package ksdn

import (
	"encoding/json"
	"fmt"
	"net"

	"k8s-ovs/cniserver"
	netutils "k8s-ovs/pkg/utils"

	"github.com/golang/glog"

	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/kubelet/dockertools"

	cnitypes "github.com/containernetworking/cni/pkg/types"
)

type podHandler interface {
	setup(req *cniserver.PodRequest) (*cnitypes.Result, error)
	update(req *cniserver.PodRequest) error
	teardown(req *cniserver.PodRequest) error
}

type podManager struct {
	// Common stuff used for both live and testing code
	podHandler podHandler
	cniServer  *cniserver.CNIServer
	// Request queue for pod operations incoming from the CNIServer
	requests chan (*cniserver.PodRequest)
	// Tracks pod :: IP address for hostport handling

	dClient dockertools.DockerInterface

	// Live pod setup/teardown stuff not used in testing code
	multitenant bool
	kClient     *kclient.Client
	vnids       *nodeVNIDMap
	ipamConfig  []byte
	mtu         uint32
}

// Creates a new live podManager; used by node code
func newPodManager(multitenant bool, localSubnetCIDR string, netInfo *NetworkInfo, kClient *kclient.Client, dClient dockertools.DockerInterface, vnids *nodeVNIDMap, mtu uint32) (*podManager, error) {
	pm := newDefaultPodManager()
	pm.multitenant = multitenant
	pm.kClient = kClient
	pm.dClient = dClient
	pm.vnids = vnids
	pm.mtu = mtu
	pm.podHandler = pm

	var err error
	pm.ipamConfig, err = getIPAMConfig(netInfo.ClusterNetwork, localSubnetCIDR)
	if err != nil {
		return nil, err
	}

	return pm, nil
}

// Creates a new basic podManager; used by testcases
func newDefaultPodManager() *podManager {
	return &podManager{
		requests: make(chan *cniserver.PodRequest, 20),
	}
}

// Generates a CNI IPAM config from a given node cluster and local subnet that
// CNI 'host-local' IPAM plugin will use to create an IP address lease for the
// container
func getIPAMConfig(clusterNetwork *net.IPNet, localSubnet string) ([]byte, error) {
	nodeNet, err := cnitypes.ParseCIDR(localSubnet)
	if err != nil {
		return nil, fmt.Errorf("error parsing node network '%s': %v", localSubnet, err)
	}

	type hostLocalIPAM struct {
		Type   string           `json:"type"`
		Subnet cnitypes.IPNet   `json:"subnet"`
		Routes []cnitypes.Route `json:"routes"`
	}

	type cniNetworkConfig struct {
		Name string         `json:"name"`
		Type string         `json:"type"`
		IPAM *hostLocalIPAM `json:"ipam"`
	}

	return json.Marshal(&cniNetworkConfig{
		Name: "k8s-ovs",
		Type: "k8s-ovs",
		IPAM: &hostLocalIPAM{
			Type: "host-local",
			Subnet: cnitypes.IPNet{
				IP:   nodeNet.IP,
				Mask: nodeNet.Mask,
			},
			Routes: []cnitypes.Route{
				{
					Dst: net.IPNet{
						IP:   net.IPv4zero,
						Mask: net.IPMask(net.IPv4zero),
					},
					GW: netutils.GenerateDefaultGateway(nodeNet),
				},
				{Dst: *clusterNetwork},
			},
		},
	})
}

// Start the CNI server and start processing requests from it
func (m *podManager) Start(socketPath string) error {
	go m.processCNIRequests()

	m.cniServer = cniserver.NewCNIServer(socketPath)
	return m.cniServer.Start(m.handleCNIRequest)
}

// Returns a key for use with the runningPods map
func getPodKey(request *cniserver.PodRequest) string {
	return fmt.Sprintf("%s/%s", request.PodNamespace, request.PodName)
}

// Add a request to the podManager CNI request queue
func (m *podManager) addRequest(request *cniserver.PodRequest) {
	m.requests <- request
}

// Wait for and return the result of a pod request
func (m *podManager) waitRequest(request *cniserver.PodRequest) *cniserver.PodResult {
	return <-request.Result
}

// Enqueue incoming pod requests from the CNI server, wait on the result,
// and return that result to the CNI client
func (m *podManager) handleCNIRequest(request *cniserver.PodRequest) ([]byte, error) {
	m.addRequest(request)
	result := m.waitRequest(request)
	glog.V(5).Infof("Returning pod network request %v, result %s err %v", request, string(result.Response), result.Err)
	return result.Response, result.Err
}

// Process all CNI requests from the request queue serially.  Our OVS interaction
// and scripts currently cannot run in parallel, and doing so greatly complicates
// setup/teardown logic
func (m *podManager) processCNIRequests() {
	for request := range m.requests {

		result := &cniserver.PodResult{}
		switch request.Command {
		case cniserver.CNI_ADD:
			ipamResult, err := m.podHandler.setup(request)
			if ipamResult != nil {
				result.Response, err = json.Marshal(ipamResult)
			}
			if err != nil {
				result.Err = err
			}
		case cniserver.CNI_UPDATE:
			result.Err = m.podHandler.update(request)
		case cniserver.CNI_DEL:
			result.Err = m.podHandler.teardown(request)
		default:
			result.Err = fmt.Errorf("unhandled CNI request %v", request.Command)
		}
		glog.V(5).Infof("Processed pod network request %v, result %s err %v", request, string(result.Response), result.Err)
		request.Result <- result
	}
	panic("stopped processing CNI pod requests!")
}
