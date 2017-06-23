package ksdn

import (
	"fmt"
	"net"
	"time"

	"github.com/golang/glog"
	"golang.org/x/net/context"

	"k8s-ovs/cniserver"
	"k8s-ovs/pkg/etcdmanager"
	"k8s-ovs/pkg/nettype"
	"k8s-ovs/pkg/ovs"
	netutils "k8s-ovs/pkg/utils"

	kapi "k8s.io/kubernetes/pkg/api"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/kubelet/dockertools"
	"k8s.io/kubernetes/pkg/labels"
	kexec "k8s.io/kubernetes/pkg/util/exec"
	kubeutilnet "k8s.io/kubernetes/pkg/util/net"
)

const (
	iptablesSyncPeriod = 30 * time.Second
	mtu                = 1450
)

type KsdnNode struct {
	multitenant        bool
	kClient            *kclient.Client
	eClient            etcdmanager.EtcdManager
	ovs                *ovs.Interface
	networkInfo        *NetworkInfo
	podManager         *podManager
	localSubnetCIDR    string
	localIP            string
	hostName           string
	podNetworkReady    chan struct{}
	vnids              *nodeVNIDMap
	iptablesSyncPeriod time.Duration
	mtu                uint32
	ctx                context.Context
}

// Called by higher layers to create the plugin SDN node instance
func StartNode(kClient *kclient.Client, eClient etcdmanager.EtcdManager, dClient dockertools.DockerInterface, network, hostname string, ctx context.Context) {

	node := &KsdnNode{
		kClient:            kClient,
		eClient:            eClient,
		ctx:                ctx,
		hostName:           hostname,
		vnids:              newNodeVNIDMap(),
		podNetworkReady:    make(chan struct{}),
		iptablesSyncPeriod: iptablesSyncPeriod,
		mtu:                mtu,
	}

	networkConfig, err := eClient.GetNetworkConfig(ctx, network)
	if err != nil {
		glog.Fatalf("Get network config failed: %v", err)
	}

	if !nettype.IsKovsNetworkPlugin(networkConfig.PluginName) {
		glog.Fatalf("Not a k8s ovs sdn plugin: %v", networkConfig.PluginName)
	}

	glog.Infof("Initializing SDN node of type %q", networkConfig.PluginName)

	node.networkInfo, err = parseNetworkInfo(networkConfig)
	if err != nil {
		glog.Fatalf("Parse network information failed: %v", err)
	}

	node.multitenant = nettype.IsKovsCloudMultitenantNetworkPlugin(networkConfig.PluginName)

	selfIP, err := netutils.GetNodeIP(hostname)
	if err != nil {
		var defaultIP net.IP
		defaultIP, err = kubeutilnet.ChooseHostInterface()
		if err != nil {
			glog.Fatalf("Get IP address failed: %v", err)
		}
		selfIP = defaultIP.String()
		glog.V(5).Infof("Resolved IP address to %q", selfIP)
	}
	node.localIP = selfIP

	ovsif, err := ovs.New(kexec.New(), BR)
	if err != nil {
		glog.Fatalf("Create ovs interface failed: %v", err)
	}
	node.ovs = ovsif

	nodeIPTables := newNodeIPTables(node.networkInfo.ClusterNetwork.String(), iptablesSyncPeriod)
	if err = nodeIPTables.Setup(); err != nil {
		glog.Fatalf("Set up iptables failed: %v", err)
	}

	node.localSubnetCIDR, err = node.getLocalSubnet()
	if err != nil {
		glog.Fatalf("Get subnet for this node failed: %v", err)
	}

	networkChanged, err := node.SetupSDN()
	if err != nil {
		glog.Fatalf("Setup network failed: %v", err)
	}

	err = node.SubnetStartNode()
	if err != nil {
		glog.Fatalf("Start subnet monitor process failed: %v", err)
	}

	if node.multitenant {
		if err = node.VnidStartNode(); err != nil {
			glog.Fatalf("Start node vnid monitor process failed: %v", err)
		}
	}

	node.podManager, err = newPodManager(node.multitenant, node.localSubnetCIDR, node.networkInfo, kClient, dClient, node.vnids, mtu)
	if err != nil {
		glog.Fatalf("Create pod manager failed: %v", err)
	}
	if err := node.podManager.Start(cniserver.CNIServerSocketPath); err != nil {
		glog.Fatalf("Start pod manager failed: %v", err)
	}

	if networkChanged {
		var pods []kapi.Pod
		pods, _, err = node.GetLocalPods(kapi.NamespaceAll)
		if err != nil {
			glog.Fatalf("Get local pods failed: %v", err)
		}
		for _, p := range pods {
			err = node.UpdatePod(p)
			if err != nil {
				glog.Warningf("Could not update pod %q: %s", p.Name, err)
			}
		}
	}

	node.markPodNetworkReady()
}

// FIXME: this should eventually go into kubelet via a CNI UPDATE/CHANGE action
// See https://github.com/containernetworking/cni/issues/89
func (node *KsdnNode) UpdatePod(pod kapi.Pod) error {
	req := &cniserver.PodRequest{
		Command:      cniserver.CNI_UPDATE,
		PodNamespace: pod.Namespace,
		PodName:      pod.Name,
		ContainerId:  getPodContainerID(&pod),
		// netns is read from docker if needed, since we don't get it from kubelet
		Result: make(chan *cniserver.PodResult),
	}

	// Send request and wait for the result
	_, err := node.podManager.handleCNIRequest(req)
	return err
}

func (node *KsdnNode) GetLocalPods(namespace string) ([]kapi.Pod, []kapi.Pod, error) {
	fieldSelector := fields.Set{"spec.nodeName": node.hostName}.AsSelector()
	opts := kapi.ListOptions{
		LabelSelector: labels.Everything(),
		FieldSelector: fieldSelector,
	}
	podList, err := node.kClient.Pods(namespace).List(opts)
	if err != nil {
		return nil, nil, err
	}

	// Filter running pods
	runPods := make([]kapi.Pod, 0, len(podList.Items))
	otherPods := make([]kapi.Pod, 0, len(podList.Items))
	for _, pod := range podList.Items {
		if pod.Status.Phase == kapi.PodRunning {
			runPods = append(runPods, pod)
		} else {
			otherPods = append(otherPods, pod)
		}
	}
	return runPods, otherPods, nil
}

func (node *KsdnNode) markPodNetworkReady() {
	close(node.podNetworkReady)
}

func (node *KsdnNode) IsPodNetworkReady() error {
	select {
	case <-node.podNetworkReady:
		return nil
	default:
		return fmt.Errorf("SDN pod network is not ready")
	}
}
