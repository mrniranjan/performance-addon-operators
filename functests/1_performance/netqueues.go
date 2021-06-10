package __performance

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpuset"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	performancev2 "github.com/openshift-kni/performance-addon-operators/api/v2"
	testutils "github.com/openshift-kni/performance-addon-operators/functests/utils"
	testclient "github.com/openshift-kni/performance-addon-operators/functests/utils/client"
	"github.com/openshift-kni/performance-addon-operators/functests/utils/cluster"
	"github.com/openshift-kni/performance-addon-operators/functests/utils/mcps"
	"github.com/openshift-kni/performance-addon-operators/functests/utils/nodes"
	"github.com/openshift-kni/performance-addon-operators/functests/utils/pods"
	"github.com/openshift-kni/performance-addon-operators/functests/utils/profiles"
	"github.com/openshift-kni/performance-addon-operators/pkg/controller/performanceprofile/components"
	machineconfigv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

var _ = Describe("[ref_id:1234][pao] updating performance profile", func() {
	var workerRTNodes []corev1.Node
	var err error
	var net bool = true
	var profile *performancev2.PerformanceProfile
	testutils.BeforeAll(func() {
		isSNO, err := cluster.IsSingleNode()
		Expect(err).ToNot(HaveOccurred())
		RunningOnSingleNode = isSNO
	})
	BeforeEach(func() {
		workerRTNodes, err = nodes.GetByLabels(testutils.NodeSelectorLabels)
		Expect(err).ToNot(HaveOccurred())
		workerRTNodes, err = nodes.MatchingOptionalSelector(workerRTNodes)
		Expect(err).ToNot(HaveOccurred())
		nodeLabel := testutils.NodeSelectorLabels
		profile, err = profiles.GetByNodeLabels(nodeLabel)
		Expect(err).ToNot(HaveOccurred())
		reservedCPUSize := getReservedCPUSize(profile.Spec.CPU)
		if reservedCPUSize < 2 {
			increaseReservedCPU(profile)
		}
	})

	Context("Updating performance profile for netqueues", func() {
		It("[test_id: 40542] Verify the number of network queues of all supported network interfaces are equal to reserved cpus count", func() {
			devices := make(map[string]int)
			if profile.Spec.Net != nil {
				By("Enable UserLevelNetworking in Profile")
				profile.Spec.Net = &performancev2.Net{
					UserLevelNetworking: &net,
				}
				profiles.UpdateWithRetry(profile)
			}
			Eventually(func() bool {
				err := checkDeviceSupport(workerRTNodes, devices)
				Expect(err).ToNot(HaveOccurred())
				for _, size := range devices {
					if size != getReservedCPUSize(profile.Spec.CPU) {
						return false
					}
				}
				return true
			}, cluster.ComputeTestTimeout(200*time.Second, RunningOnSingleNode), testPollInterval*time.Second).Should(BeTrue())
			err := checkDeviceSupport(workerRTNodes, devices)
			Expect(err).ToNot(HaveOccurred())
			for _, size := range devices {
				Expect(size).Should(Equal(getReservedCPUSize(profile.Spec.CPU)))
			}
			//Verify the tuned profile is created on the worker-cnf nodes:
			tunedCmd := []string{"tuned-adm", "profile_info", "openshift-node-performance-performance"}
			for _, node := range workerRTNodes {
				tunedPod := getTunedPod(&node)
				_, err := pods.ExecCommandOnPod(tunedPod, tunedCmd)
				Expect(err).ToNot(HaveOccurred())
			}

		})

		It("[test_id: 40543] Add interfaceName and verify the interface netqueues are equal to reserved cpus count.", func() {
			devices := make(map[string]int)
			device := ""
			if profile.Spec.Net != nil {
				err := checkDeviceSupport(workerRTNodes, devices)
				Expect(err).ToNot(HaveOccurred())
				for d := range devices {
					device = d
				}
				if profile.Spec.Net.UserLevelNetworking != nil && *profile.Spec.Net.UserLevelNetworking && len(profile.Spec.Net.Devices) == 0 {
					By("Enable UserLevelNetworking and add Devices in Profile")
					profile.Spec.Net = &performancev2.Net{
						UserLevelNetworking: &net,
						Devices: []performancev2.Device{
							{
								InterfaceName: &device,
							},
						},
					}
					profiles.UpdateWithRetry(profile)
					Eventually(func() bool {
						err := checkDeviceSupport(workerRTNodes, devices)
						Expect(err).ToNot(HaveOccurred())
						for _, size := range devices {
							if size != getReservedCPUSize(profile.Spec.CPU) {
								return false
							}
						}
						return true
					}, cluster.ComputeTestTimeout(200*time.Second, RunningOnSingleNode), testPollInterval*time.Second).Should(BeTrue())
				}
				//Verify the tuned profile is created on the worker-cnf nodes:
				tunedCmd := []string{"bash", "-c",
					fmt.Sprintf("cat /etc/tuned/openshift-node-performance-performance/tuned.conf | grep devices_udev_regex")}

				for _, node := range workerRTNodes {
					tunedPod := getTunedPod(&node)
					out, err := pods.ExecCommandOnPod(tunedPod, tunedCmd)
					deviceExists := strings.ContainsAny(string(out), device)
					Expect(deviceExists).To(BeTrue())
					Expect(err).ToNot(HaveOccurred())
				}
			}
		})

		It("[test_id: 40545] Verify reserved cpus count is applied to specific supported networking devices using wildcard matches", func() {
			devices := make(map[string]int)
			var device, devicePattern string
			if profile.Spec.Net != nil {
				err := checkDeviceSupport(workerRTNodes, devices)
				Expect(err).ToNot(HaveOccurred())
				for d := range devices {
					device = d
				}
				devicePattern = device[:len(device)-1] + "*"
				if profile.Spec.Net.UserLevelNetworking != nil && *profile.Spec.Net.UserLevelNetworking && len(profile.Spec.Net.Devices) != 0 {
					By("Enable UserLevelNetworking and add Devices in Profile")
					profile.Spec.Net = &performancev2.Net{
						UserLevelNetworking: &net,
						Devices: []performancev2.Device{
							{
								InterfaceName: &devicePattern,
							},
						},
					}
					profiles.UpdateWithRetry(profile)
					Eventually(func() bool {
						err := checkDeviceSupport(workerRTNodes, devices)
						Expect(err).ToNot(HaveOccurred())
						for _, size := range devices {
							if size != getReservedCPUSize(profile.Spec.CPU) {
								return false
							}
						}
						return true
					}, cluster.ComputeTestTimeout(200*time.Second, RunningOnSingleNode), testPollInterval*time.Second).Should(BeTrue())
				}
				//Verify the tuned profile is created on the worker-cnf nodes:
				tunedCmd := []string{"bash", "-c",
					fmt.Sprintf("cat /etc/tuned/openshift-node-performance-performance/tuned.conf | grep devices_udev_regex")}

				for _, node := range workerRTNodes {
					tunedPod := getTunedPod(&node)
					out, err := pods.ExecCommandOnPod(tunedPod, tunedCmd)
					deviceExists := strings.ContainsAny(string(out), device)
					Expect(deviceExists).To(BeTrue())
					Expect(err).ToNot(HaveOccurred())
				}
			}
		})

		It("[test_id: 40668] Verify reserved cpu count is added to networking devices matched with vendor and Device id", func() {
			devices := make(map[string]int)
			var device, vid, did string
			if profile.Spec.Net != nil {
				err := checkDeviceSupport(workerRTNodes, devices)
				Expect(err).ToNot(HaveOccurred())
				for d := range devices {
					device = d
				}
				for _, node := range workerRTNodes {
					vid = getVendorID(node, device)
					did = getDeviceID(node, device)
				}
				if profile.Spec.Net.UserLevelNetworking != nil && *profile.Spec.Net.UserLevelNetworking && len(profile.Spec.Net.Devices) != 0 {
					By("Enable UserLevelNetworking and add DeviceID, VendorID and Interface in Profile")
					profile.Spec.Net = &performancev2.Net{
						UserLevelNetworking: &net,
						Devices: []performancev2.Device{
							{
								InterfaceName: &device,
								VendorID:      &vid,
								DeviceID:      &did,
							},
						},
					}
					profiles.UpdateWithRetry(profile)
					Eventually(func() bool {
						err := checkDeviceSupport(workerRTNodes, devices)
						Expect(err).ToNot(HaveOccurred())
						for _, size := range devices {
							if size != getReservedCPUSize(profile.Spec.CPU) {
								return false
							}
						}
						return true
					}, cluster.ComputeTestTimeout(240*time.Second, RunningOnSingleNode), testPollInterval*time.Second).Should(BeTrue())
				}
				//Verify the tuned profile is created on the worker-cnf nodes:
				tunedCmd := []string{"bash", "-c",
					fmt.Sprintf("cat /etc/tuned/openshift-node-performance-performance/tuned.conf | grep devices_udev_regex")}
				for _, node := range workerRTNodes {
					tunedPod := getTunedPod(&node)
					out, err := pods.ExecCommandOnPod(tunedPod, tunedCmd)
					deviceExists := strings.ContainsAny(string(out), device)
					Expect(deviceExists).To(BeTrue())
					Expect(err).ToNot(HaveOccurred())
				}
			}
		})
	})
})

//Check if the device support multiple queues
func checkDeviceSupport(workernodes []corev1.Node, devices map[string]int) error {
	cmdGetPhysicalDevices := []string{"find", "/sys/class/net", "-type", "l", "-not", "-lname", "*virtual*", "-printf", "%f "}
	var channelCurrentCombined int = 0
	var err error
	for _, node := range workernodes {
		tunedPod := getTunedPod(&node)
		phyDevs, err := pods.ExecCommandOnPod(tunedPod, cmdGetPhysicalDevices)
		Expect(err).ToNot(HaveOccurred())
		for _, d := range strings.Split(string(phyDevs), " ") {
			if d == "" {
				continue
			}
			_, err := pods.ExecCommandOnPod(tunedPod, []string{"ethtool", "-l", d})
			if err == nil {
				cmdCombinedChannelsCurrent := []string{"bash", "-c",
					fmt.Sprintf("ethtool -l %s | sed -n '/Current hardware settings:/,/Combined:/{s/^Combined:\\s*//p}'", d)}
				out, err := pods.ExecCommandOnPod(tunedPod, cmdCombinedChannelsCurrent)
				channelCurrentCombined, err = strconv.Atoi(strings.TrimSpace(string(out)))
				Expect(err).ToNot(HaveOccurred())
				if channelCurrentCombined == 1 {
					fmt.Printf("Device %s doesn't support multiple queues\n", d)
				} else {
					fmt.Printf("Device %s supports multiple queues\n", d)
					devices[d] = channelCurrentCombined
				}
			}
		}
	}
	return err
}

//Get Tuned Pods
func getTunedPod(node *corev1.Node) *corev1.Pod {
	listOptions := &client.ListOptions{
		Namespace:     components.NamespaceNodeTuningOperator,
		FieldSelector: fields.SelectorFromSet(fields.Set{"spec.nodeName": node.Name}),
		LabelSelector: labels.SelectorFromSet(labels.Set{"openshift-app": "tuned"}),
	}
	tunedList := &corev1.PodList{}
	Eventually(func() bool {
		if err := testclient.Client.List(context.TODO(), tunedList, listOptions); err != nil {
			return false
		}
		if len(tunedList.Items) == 0 {
			return false
		}
		for _, s := range tunedList.Items[0].Status.ContainerStatuses {
			if s.Ready == false {
				return false
			}
		}
		return true
	}, cluster.ComputeTestTimeout(testTimeout, RunningOnSingleNode), testPollInterval*time.Second).Should(BeTrue(),
		"there should be one tuned daemon per node")
	return &tunedList.Items[0]
}

//Get Reserved CPU Count
func getReservedCPUSize(CPU *performancev2.CPU) int {

	reservedCPUs, err := cpuset.Parse(string(*CPU.Reserved))
	Expect(err).ToNot(HaveOccurred())
	return reservedCPUs.Size()

}

// Increase reserved cpu count
func increaseReservedCPU(profile *performancev2.PerformanceProfile) {
	performanceMCP, err := mcps.GetByProfile(profile)
	Expect(err).ToNot(HaveOccurred())
	reserved := performancev2.CPUSet("0-1")
	isolated := performancev2.CPUSet("2-4")
	f := false
	By("Modifying profile")
	profile.Spec.CPU = &performancev2.CPU{
		BalanceIsolated: &f,
		Reserved:        &reserved,
		Isolated:        &isolated,
	}
	By("Applying changes in performance profile and waiting until mcp will start updating")
	profiles.UpdateWithRetry(profile)
	mcps.WaitForCondition(performanceMCP, machineconfigv1.MachineConfigPoolUpdating, corev1.ConditionTrue)
	By("Waiting when mcp finishes updates")
	mcps.WaitForCondition(performanceMCP, machineconfigv1.MachineConfigPoolUpdated, corev1.ConditionTrue)

}

//Get VendorID
func getVendorID(node corev1.Node, device string) string {
	cmd := []string{"bash", "-c",
		fmt.Sprintf("cat /sys/class/net/%s/device/vendor", device)}
	stdout, err := nodes.ExecCommandOnNode(cmd, &node)
	Expect(err).ToNot(HaveOccurred())
	return stdout
}

//Get DeviceID
func getDeviceID(node corev1.Node, device string) string {
	cmd := []string{"bash", "-c",
		fmt.Sprintf("cat /sys/class/net/%s/device/device", device)}
	stdout, err := nodes.ExecCommandOnNode(cmd, &node)
	Expect(err).ToNot(HaveOccurred())
	return stdout
}
