package ksdn

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/golang/glog"

	"k8s-ovs/pkg/etcdmanager"

	kapi "k8s.io/kubernetes/pkg/api"
	kcache "k8s.io/kubernetes/pkg/client/cache"
	"k8s.io/kubernetes/pkg/fields"
	kcontainer "k8s.io/kubernetes/pkg/kubelet/container"
)

func getPodContainerID(pod *kapi.Pod) string {
	if len(pod.Status.ContainerStatuses) > 0 {
		return kcontainer.ParseContainerID(pod.Status.ContainerStatuses[0].ContainerID).ID
	}
	return ""
}

func hostSubnetToString(subnet *etcdmanager.HostSubnet) string {
	return fmt.Sprintf("ip: %q, subnet: %q", subnet.Host, subnet.HostIP, subnet.Subnet)
}

func clusterNetworkToString(n *etcdmanager.ClusterNetwork) string {
	return fmt.Sprintf("network: %q, hostSubnetBits: %d, serviceNetwork: %q, pluginName: %q", n.Network, n.HostSubnetLength, n.ServiceNetwork, n.PluginName)
}

type NetworkInfo struct {
	ClusterNetwork *net.IPNet
	ServiceNetwork *net.IPNet
	name           string
}

func parseNetworkInfo(n *etcdmanager.ClusterNetwork) (*NetworkInfo, error) {
	_, cn, err := net.ParseCIDR(n.Network)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse ClusterNetwork CIDR %s: %v", n.Network, err)
	}
	_, sn, err := net.ParseCIDR(n.ServiceNetwork)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse ServiceNetwork CIDR %s: %v", n.ServiceNetwork, err)
	}

	return &NetworkInfo{
		ClusterNetwork: cn,
		ServiceNetwork: sn,
		name:           n.Name,
	}, nil
}

func (ni *NetworkInfo) validateNodeIP(nodeIP string) error {
	if nodeIP == "" || nodeIP == "127.0.0.1" {
		return fmt.Errorf("Invalid node IP %q", nodeIP)
	}

	// Ensure each node's NodeIP is not contained by the cluster network,
	// which could cause a routing loop. (rhbz#1295486)
	ipaddr := net.ParseIP(nodeIP)
	if ipaddr == nil {
		return fmt.Errorf("Failed to parse node IP %s", nodeIP)
	}

	if ni.ClusterNetwork.Contains(ipaddr) {
		return fmt.Errorf("Node IP %s conflicts with cluster network %s", nodeIP, ni.ClusterNetwork.String())
	}
	if ni.ServiceNetwork.Contains(ipaddr) {
		return fmt.Errorf("Node IP %s conflicts with service network %s", nodeIP, ni.ServiceNetwork.String())
	}

	return nil
}

type ResourceName string

const (
	Nodes                 ResourceName = "Nodes"
	Namespaces            ResourceName = "Namespaces"
	Services              ResourceName = "Services"
	Pods                  ResourceName = "Pods"
)

// Run event queue for the given resource. The 'process' function is called
// repeatedly with each available cache.Delta that describes state changes
// to an object. If the process function returns an error queued changes
// for that object are dropped but processing continues with the next available
// object's cache.Deltas.  The error is logged with call stack information.
func runEventQueueForResource(client kcache.Getter, resourceName ResourceName, expectedType interface{}, selector fields.Selector, process ProcessEventFunc) {
	rn := strings.ToLower(string(resourceName))
	lw := kcache.NewListWatchFromClient(client, rn, kapi.NamespaceAll, selector)
	eventQueue := NewEventQueue(DeletionHandlingMetaNamespaceKeyFunc)
	// Repopulate event queue every 30 mins
	// Existing items in the event queue will have watch.Modified event type
	kcache.NewReflector(lw, expectedType, eventQueue, 30*time.Minute).Run()

	// Run the queue
	for {
		eventQueue.Pop(process, expectedType)
	}
}

// Run event queue for the given resource.
// NOTE: this function will handle DeletedFinalStateUnknown delta objects
// automatically, which may not always be what you want since the now-deleted
// object may be stale.
func RunEventQueue(client kcache.Getter, resourceName ResourceName, process ProcessEventFunc) {
	var expectedType interface{}

	switch resourceName {
	case Nodes:
		expectedType = &kapi.Node{}
	case Namespaces:
		expectedType = &kapi.Namespace{}
	case Services:
		expectedType = &kapi.Service{}
	case Pods:
		expectedType = &kapi.Pod{}
	default:
		glog.Fatalf("Unknown resource %s during initialization of event queue", resourceName)
	}

	runEventQueueForResource(client, resourceName, expectedType, fields.Everything(), process)
}
