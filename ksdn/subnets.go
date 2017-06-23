package ksdn

import (
	"fmt"
	"net"

	"github.com/golang/glog"

	kapi "k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/client/cache"
	"k8s.io/kubernetes/pkg/types"
	"k8s.io/kubernetes/pkg/util/sets"
	utilwait "k8s.io/kubernetes/pkg/util/wait"

	. "k8s-ovs/pkg/etcdmanager"
	"k8s-ovs/pkg/snalloc"
	netutils "k8s-ovs/pkg/utils"
)

func (master *KsdnMaster) SubnetStartMaster(clusterNetwork *net.IPNet, hostSubnetLength uint32) error {
	subrange := make([]string, 0)
	subnets, err := master.eClient.GetSubnets(master.ctx, master.networkInfo.name)
	if err != nil {
		glog.Errorf("Error in initializing/fetching subnets: %v", err)
		return err
	}

	for _, sub := range subnets {
		subrange = append(subrange, sub.Subnet)
		if err = master.networkInfo.validateNodeIP(sub.HostIP); err != nil {
			glog.Errorf("Failed to validate HostSubnet %s: %v", sub.Subnet, err)
		} else {
			glog.Infof("Found existing HostSubnet %s", sub.Subnet)
		}
	}

	master.subnetAllocator, err = snalloc.NewSubnetAllocator(clusterNetwork.String(), hostSubnetLength, subrange)
	if err != nil {
		return err
	}

	go utilwait.Forever(master.watchNodes, 0)
	go utilwait.Forever(master.watchSubnets, 0)
	return nil
}

func (master *KsdnMaster) addNode(nodeName string, nodeIP string, assign bool) error {
	// Validate node IP before proceeding
	if err := master.networkInfo.validateNodeIP(nodeIP); err != nil {
		return err
	}
	sub, err := master.eClient.GetSubnet(master.ctx, master.networkInfo.name, nodeIP)
	if err == nil {
		if sub.Host == nodeName && sub.Assign == assign {
			return nil
		} else {
			sub.Host = nodeName
			sub.Assign = assign
			err = master.eClient.RenewSubnet(master.ctx, master.networkInfo.name, sub)
			if err != nil {
				return fmt.Errorf("Error updating subnet %s for node %s: %v", sub.Subnet, nodeName, err)
			}
			glog.Infof("Updated HostSubnet %s", sub.Subnet)
			return nil
		}
	}

	// Create new subnet
	sn, err := master.subnetAllocator.GetNetwork()
	if err != nil {
		return fmt.Errorf("Error allocating network for node %s: %v", nodeName, err)
	}

	sub = &HostSubnet{
		Host:   nodeName,
		HostIP: nodeIP,
		Subnet: sn.String(),
		Assign: assign,
	}

	err = master.eClient.AcquireSubnet(master.ctx, master.networkInfo.name, nodeIP, sub)
	if err != nil {
		master.subnetAllocator.ReleaseNetwork(sn)
		return fmt.Errorf("Error creating subnet %s for node %s: %v", sn.String(), nodeName, err)
	}

	glog.Infof("Created HostSubnet %v done!", sub)
	return nil
}

func (master *KsdnMaster) deleteNode(nodeName string) error {
	err := master.eClient.RevokeSubnet(master.ctx, master.networkInfo.name, nodeName)
	if err != nil {
		return fmt.Errorf("Error delete subnet for node %s: %v", nodeName, err)
	}

	glog.Infof("Delete Host %v done!", nodeName)
	return nil
}

func isValidNodeIP(node *kapi.Node, nodeIP string) bool {
	for _, addr := range node.Status.Addresses {
		if addr.Address == nodeIP {
			return true
		}
	}
	return false
}

func getNodeIP(node *kapi.Node) (string, error) {
	if len(node.Status.Addresses) > 0 && node.Status.Addresses[0].Address != "" {
		return node.Status.Addresses[0].Address, nil
	} else {
		return netutils.GetNodeIP(node.Name)
	}
}

func (master *KsdnMaster) watchNodes() {
	nodeAddressMap := map[types.UID]string{}
	RunEventQueue(master.kClient, Nodes, func(delta cache.Delta) error {
		node := delta.Object.(*kapi.Node)
		name := node.ObjectMeta.Name
		uid := node.ObjectMeta.UID

		nodeIP, err := getNodeIP(node)
		if err != nil {
			return fmt.Errorf("failed to get node IP for %s, skipping event: %v, node: %v", name, delta.Type, node)
		}

		switch delta.Type {
		case cache.Sync, cache.Added, cache.Updated:

			if oldNodeIP, ok := nodeAddressMap[uid]; ok && ((nodeIP == oldNodeIP) || isValidNodeIP(node, oldNodeIP)) {
				break
			}
			// Node status is frequently updated by kubelet, so log only if the above condition is not met
			glog.Infof("Watch %s event for Node %q", delta.Type, name)

			err = master.addNode(name, nodeIP, false)
			if err != nil {
				return fmt.Errorf("error creating subnet for node %s, ip %s: %v", name, nodeIP, err)
			}
			nodeAddressMap[uid] = nodeIP
		case cache.Deleted:
			glog.Infof("Watch %s event for Node %q", delta.Type, name)
			delete(nodeAddressMap, uid)

			err = master.deleteNode(nodeIP)
			if err != nil {
				return fmt.Errorf("Error deleting node %s: %v", nodeIP, err)
			}
		}
		return nil
	})
}

func (master *KsdnMaster) masterHandleSubnetEvent(batch []Event) {
	for _, evt := range batch {
		name := evt.Subnet.Host
		hostIP := evt.Subnet.HostIP
		subnet := evt.Subnet.Subnet
		assign := evt.Subnet.Assign

		switch evt.Type {
		case EventAdded:
			if assign {
				glog.Infof("Master get subnet %v added event", evt.Subnet)
				err := master.eClient.RevokeSubnet(master.ctx, master.networkInfo.name, hostIP)
				if err != nil {
					glog.Errorf("Error deleting subnet for node %s: %v", hostIP, err)
					continue
				}
				err = master.addNode(name, hostIP, false)
				if err != nil {
					glog.Errorf("Error creating subnet for node %s, ip %s: %v", name, hostIP, err)
					continue
				}
			}

		case EventRemoved:
			if !assign {
				glog.Info("Master get subnet %v  removed event", evt.Subnet)
				_, ipnet, err := net.ParseCIDR(subnet)
				if err != nil {
					glog.Errorf("Error parsing subnet %q for node %q for deletion: %v", subnet, hostIP, err)
				} else {
					master.subnetAllocator.ReleaseNetwork(ipnet)
				}
			}
		default:
			glog.Error("Internal error: unknown event type: ", int(evt.Type))
		}
	}
}

// Only run on the master
// Watch for all hostsubnet events and if one is found with the right annotation, use the SubnetAllocator to dole a real subnet
func (master *KsdnMaster) watchSubnets() {
	receiver := make(chan []Event)
	RunSubnetWatch(master.ctx, master.eClient, master.networkInfo.name, receiver, master.masterHandleSubnetEvent)
}

func (node *KsdnNode) nodeHandleSubnetEvent(batch []Event) {
	subnets := sets.NewString()
	for _, evt := range batch {
		if node.localIP == evt.Subnet.HostIP {
			continue
		}
		switch evt.Type {
		case EventAdded:
			if subnets.Has(evt.Subnet.HostIP) {
				glog.Warningf("Ignoring invalid subnet %v for node %s", subnets, evt.Subnet.HostIP)
				continue
			}
			if err := node.networkInfo.validateNodeIP(evt.Subnet.HostIP); err != nil {
				glog.Warningf("Ignoring invalid subnet for node %s: %v", evt.Subnet.HostIP, err)
				continue
			}

			if err := node.AddHostSubnetRules(&evt.Subnet); err != nil {
				glog.Warning("AddHostSubnetRules error: %v", err)
				continue
			}
			glog.Infof("Subnet(%v) rules added", evt.Subnet)
			subnets.Insert(evt.Subnet.HostIP)

		case EventRemoved:
			subnets.Delete(evt.Subnet.HostIP)
			if err := node.DeleteHostSubnetRules(&evt.Subnet); err != nil {
				glog.Warning("DeleteHostSubnetRules error: %v", err)
				continue
			}
			glog.Info("Subnet(%v) rules removed: %v", evt.Subnet)

		default:
			glog.Error("Internal error: unknown event type: ", int(evt.Type))
		}
	}
}

func (node *KsdnNode) SubnetStartNode() error {
	go utilwait.Forever(node.watchSubnets, 0)
	return nil
}

// Only run on the node
func (node *KsdnNode) watchSubnets() {
	receiver := make(chan []Event)
	RunSubnetWatch(node.ctx, node.eClient, node.networkInfo.name, receiver, node.nodeHandleSubnetEvent)
}
