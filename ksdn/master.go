package ksdn

import (
	"github.com/golang/glog"
	"golang.org/x/net/context"

	"k8s-ovs/pkg/etcdmanager"
	"k8s-ovs/pkg/nettype"
	"k8s-ovs/pkg/snalloc"

	kclient "k8s.io/kubernetes/pkg/client/unversioned"
)

type KsdnMaster struct {
	kClient         *kclient.Client
	eClient         etcdmanager.EtcdManager
	ctx             context.Context
	networkInfo     *NetworkInfo
	subnetAllocator *snalloc.SubnetAllocator
	vnids           *masterVNIDMap
}

func StartMaster(kClient *kclient.Client, eClient etcdmanager.EtcdManager, network string, ctx context.Context) error {

	master := &KsdnMaster{
		kClient: kClient,
		eClient: eClient,
		ctx:     ctx,
	}

	networkConfig, err := master.eClient.GetNetworkConfig(ctx, network)
	if err != nil {
		return err
	}

	if !nettype.IsKovsNetworkPlugin(networkConfig.PluginName) {
		return nil
	}

	glog.Infof("Initializing SDN master of type %q", networkConfig.PluginName)

	master.networkInfo, err = parseNetworkInfo(networkConfig)
	if err != nil {
		return err
	}

	if err = master.SubnetStartMaster(master.networkInfo.ClusterNetwork, networkConfig.HostSubnetLength); err != nil {
		return err
	}

	if nettype.IsKovsCloudMultitenantNetworkPlugin(networkConfig.PluginName) {
		master.vnids = newMasterVNIDMap()

		if err = master.VnidStartMaster(); err != nil {
			return err
		}
	}

	return nil
}
