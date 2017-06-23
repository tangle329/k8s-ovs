package ksdn

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/golang/glog"

	kapi "k8s.io/kubernetes/pkg/api"
	kcontainer "k8s.io/kubernetes/pkg/kubelet/container"
	"k8s.io/kubernetes/pkg/kubelet/dockertools"
	knetwork "k8s.io/kubernetes/pkg/kubelet/network"
	kbandwidth "k8s.io/kubernetes/pkg/util/bandwidth"

	"github.com/containernetworking/cni/pkg/invoke"
	"github.com/containernetworking/cni/pkg/ip"
	"github.com/containernetworking/cni/pkg/ipam"
	"github.com/containernetworking/cni/pkg/ns"
	cnitypes "github.com/containernetworking/cni/pkg/types"

	"github.com/vishvananda/netlink"
	"k8s-ovs/cniserver"
)

const (
	sdnScript   = "k8s-sdn-ovs"
	setUpCmd    = "setup"
	tearDownCmd = "teardown"
	updateCmd   = "update"

	podInterfaceName = knetwork.DefaultInterfaceName
)

type PodConfig struct {
	vnid             uint32
	ingressBandwidth string
	egressBandwidth  string
}

func getBandwidth(pod *kapi.Pod) (string, string, error) {
	ingress, egress, err := kbandwidth.ExtractPodBandwidthResources(pod.Annotations)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse pod bandwidth: %v", err)
	}
	var ingressStr, egressStr string
	if ingress != nil {
		ingressStr = fmt.Sprintf("%d", ingress.Value())
	}
	if egress != nil {
		egressStr = fmt.Sprintf("%d", egress.Value())
	}
	return ingressStr, egressStr, nil
}

// Create and return a PodConfig describing which k8s-ovs specific pod attributes
// to configure
func (m *podManager) getPodConfig(req *cniserver.PodRequest) (*PodConfig, *kapi.Pod, error) {
	var err error

	config := &PodConfig{}
	if m.multitenant {
		config.vnid, err = m.vnids.GetVNID(req.PodNamespace)
		if err != nil {
			return nil, nil, err
		}
	}

	pod, err := m.kClient.Pods(req.PodNamespace).Get(req.PodName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read pod %s/%s: %v", req.PodNamespace, req.PodName, err)
	}

	config.ingressBandwidth, config.egressBandwidth, err = getBandwidth(pod)
	if err != nil {
		return nil, nil, err
	}

	return config, pod, nil
}

// For a given container, returns host veth name, container veth MAC, and pod IP
func getVethInfo(netns, containerIfname string) (string, string, string, error) {
	var (
		peerIfindex int
		contVeth    netlink.Link
		err         error
		podIP       string
	)

	containerNs, err := ns.GetNS(netns)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to get container netns: %v", err)
	}
	defer containerNs.Close()

	err = containerNs.Do(func(ns.NetNS) error {
		contVeth, err = netlink.LinkByName(containerIfname)
		if err != nil {
			return err
		}
		peerIfindex = contVeth.Attrs().ParentIndex

		addrs, err := netlink.AddrList(contVeth, syscall.AF_INET)
		if err != nil {
			return fmt.Errorf("failed to get container IP addresses: %v", err)
		}
		if len(addrs) == 0 {
			return fmt.Errorf("container had no addresses")
		}
		podIP = addrs[0].IP.String()

		return nil
	})
	if err != nil {
		return "", "", "", fmt.Errorf("failed to inspect container interface: %v", err)
	}

	hostVeth, err := netlink.LinkByIndex(peerIfindex)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to get host veth: %v", err)
	}

	return hostVeth.Attrs().Name, contVeth.Attrs().HardwareAddr.String(), podIP, nil
}

func createIPAMArgs(netnsPath string, action cniserver.CNICommand, id string) *invoke.Args {
	return &invoke.Args{
		Command:     string(action),
		ContainerID: id,
		NetNS:       netnsPath,
		IfName:      podInterfaceName,
		Path:        "/opt/cni/bin",
	}
}

// Run CNI IPAM allocation for the container and return the allocated IP address
func (m *podManager) ipamAdd(netnsPath string, id string) (*cnitypes.Result, error) {
	if netnsPath == "" {
		return nil, fmt.Errorf("netns required for CNI_ADD")
	}

	args := createIPAMArgs(netnsPath, cniserver.CNI_ADD, id)
	result, err := invoke.ExecPluginWithResult("/opt/cni/bin/host-local", m.ipamConfig, args)
	if err != nil {
		return nil, fmt.Errorf("failed to run CNI IPAM ADD: %v", err)
	}

	if result.IP4 == nil {
		return nil, fmt.Errorf("failed to obtain IP address from CNI IPAM")
	}

	return result, nil
}

// Run CNI IPAM release for the container
func (m *podManager) ipamDel(id string) error {
	args := createIPAMArgs("", cniserver.CNI_DEL, id)
	err := invoke.ExecPluginWithoutResult("/opt/cni/bin/host-local", m.ipamConfig, args)
	if err != nil {
		return fmt.Errorf("failed to run CNI IPAM DEL: %v", err)
	}
	return nil
}

func isScriptError(err error) bool {
	_, ok := err.(*exec.ExitError)
	return ok
}

// Get the last command (which is prefixed with "+" because of "set -x") and its output
func getScriptError(output []byte) string {
	lines := strings.Split(string(output), "\n")
	for n := len(lines) - 1; n >= 0; n-- {
		if strings.HasPrefix(lines[n], "+") {
			return strings.Join(lines[n:], "\n")
		}
	}
	return string(output)
}

func vnidToString(vnid uint32) string {
	return strconv.FormatUint(uint64(vnid), 10)
}

// Set up all networking (host/container veth, OVS flows, IPAM, loopback, etc)
func (m *podManager) setup(req *cniserver.PodRequest) (*cnitypes.Result, error) {
	podConfig, _, err := m.getPodConfig(req)
	if err != nil {
		return nil, err
	}

	ipamResult, err := m.ipamAdd(req.Netns, req.ContainerId)
	if err != nil {
		return nil, fmt.Errorf("failed to run IPAM for %v: %v", req.ContainerId, err)
	}
	podIP := ipamResult.IP4.IP.IP

	// Release any IPAM allocations and hostports if the setup failed
	var success bool
	defer func() {
		if !success {
			m.ipamDel(req.ContainerId)
		}
	}()

	var hostVeth, contVeth netlink.Link
	err = ns.WithNetNSPath(req.Netns, func(hostNS ns.NetNS) error {
		hostVeth, contVeth, err = ip.SetupVeth(podInterfaceName, int(m.mtu), hostNS)
		if err != nil {
			return fmt.Errorf("failed to create container veth: %v", err)
		}
		// refetch to get hardware address and other properties
		contVeth, err = netlink.LinkByIndex(contVeth.Attrs().Index)
		if err != nil {
			return fmt.Errorf("failed to fetch container veth: %v", err)
		}

		// Clear out gateway to prevent ConfigureIface from adding the cluster
		// subnet via the gateway
		ipamResult.IP4.Gateway = nil
		if err = ipam.ConfigureIface(podInterfaceName, ipamResult); err != nil {
			return fmt.Errorf("failed to configure container IPAM: %v", err)
		}

		lo, err := netlink.LinkByName("lo")
		if err == nil {
			err = netlink.LinkSetUp(lo)
		}
		if err != nil {
			return fmt.Errorf("failed to configure container loopback: %v", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	contVethMac := contVeth.Attrs().HardwareAddr.String()
	vnidStr := vnidToString(podConfig.vnid)
	out, err := exec.Command(sdnScript, setUpCmd, hostVeth.Attrs().Name, contVethMac, podIP.String(), vnidStr, podConfig.ingressBandwidth, podConfig.egressBandwidth).CombinedOutput()
	glog.V(5).Infof("SetUpPod network plugin output: %s, %v", string(out), err)

	if isScriptError(err) {
		return nil, fmt.Errorf("error running network setup script:\nhostVethName %s, contVethMac %s, podIP %s, podConfig %#v\n %s", hostVeth.Attrs().Name, contVethMac, podIP.String(), podConfig, getScriptError(out))
	} else if err != nil {
		return nil, err
	}

	success = true
	return ipamResult, nil
}

func (m *podManager) getContainerNetnsPath(id string) (string, error) {
	inspectResult, err := m.dClient.InspectContainer(kcontainer.DockerID(id).ContainerID().ID)
	if err != nil {
		glog.Errorf("Error inspecting container: '%v'", err)
		return "", err
	}
	netnsPath := fmt.Sprintf(dockertools.DockerNetnsFmt, inspectResult.State.Pid)
	return netnsPath, nil
}

// Update OVS flows when something (like the pod's namespace VNID) changes
func (m *podManager) update(req *cniserver.PodRequest) error {
	// Updates may come at startup and thus we may not have the pod's
	// netns from kubelet (since kubelet doesn't have UPDATE actions).
	// Read the missing netns from the pod's file.
	if req.Netns == "" {
		netns, err := m.getContainerNetnsPath(req.ContainerId)
		if err != nil {
			return err
		}
		req.Netns = netns
		glog.V(5).Infof("get netns:%v for container:%v", netns, req.ContainerId)
	}

	podConfig, _, err := m.getPodConfig(req)
	if err != nil {
		return err
	}

	hostVethName, contVethMac, podIP, err := getVethInfo(req.Netns, podInterfaceName)
	if err != nil {
		return err
	}

	vnidStr := vnidToString(podConfig.vnid)
	out, err := exec.Command(sdnScript, updateCmd, hostVethName, contVethMac, podIP, vnidStr, podConfig.ingressBandwidth, podConfig.egressBandwidth).CombinedOutput()
	glog.V(5).Infof("UpdatePod network plugin output: %s, %v", string(out), err)

	if isScriptError(err) {
		return fmt.Errorf("error running network update script: %s", getScriptError(out))
	} else if err != nil {
		return err
	}

	return nil
}

// Clean up all pod networking (clear OVS flows, release IPAM lease, remove host/container veth)
func (m *podManager) teardown(req *cniserver.PodRequest) error {
	netnsValid := true
	if err := ns.IsNSorErr(req.Netns); err != nil {
		if _, ok := err.(ns.NSPathNotExistErr); ok {
			glog.V(3).Infof("teardown called on already-destroyed pod %s/%s; only cleaning up IPAM", req.PodNamespace, req.PodName)
			netnsValid = false
		}
	}

	if netnsValid {
		hostVethName, contVethMac, podIP, err := getVethInfo(req.Netns, podInterfaceName)
		if err != nil {
			return err
		}

		// The script's teardown functionality doesn't need the VNID
		out, err := exec.Command(sdnScript, tearDownCmd, hostVethName, contVethMac, podIP, "-1").CombinedOutput()
		glog.V(5).Infof("TearDownPod network plugin output: %s, %v", string(out), err)

		if isScriptError(err) {
			return fmt.Errorf("error running network teardown script: %s", getScriptError(out))
		} else if err != nil {
			return err
		}
	}

	if err := m.ipamDel(req.ContainerId); err != nil {
		return err
	}

	return nil
}
