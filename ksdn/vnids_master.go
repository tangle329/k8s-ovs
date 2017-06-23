package ksdn

import (
	"fmt"
	"sync"

	"github.com/golang/glog"
	"golang.org/x/net/context"

	kapi "k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/client/cache"
	"k8s.io/kubernetes/pkg/util/sets"
	utilwait "k8s.io/kubernetes/pkg/util/wait"

	. "k8s-ovs/pkg/etcdmanager"
	"k8s-ovs/pkg/utils"
	"k8s-ovs/pkg/vnid"
	pnetid "k8s-ovs/pkg/vnid/netid"
)

type masterVNIDMap struct {
	// Synchronizes assign, revoke and update VNID
	lock         sync.Mutex
	ids          map[string]uint32
	netIDManager *pnetid.Allocator

	adminNamespaces sets.String
}

func newMasterVNIDMap() *masterVNIDMap {
	netIDRange, err := pnetid.NewNetIDRange(vnid.MinVNID, vnid.MaxVNID)
	if err != nil {
		panic(err)
	}

	return &masterVNIDMap{
		netIDManager:    pnetid.NewInMemory(netIDRange),
		adminNamespaces: sets.NewString(utils.SdnNamespace),
		ids:             make(map[string]uint32),
	}
}

func (vmap *masterVNIDMap) getVNID(name string) (uint32, bool) {
	id, found := vmap.ids[name]
	return id, found
}

func (vmap *masterVNIDMap) setVNID(name string, id uint32) {
	vmap.ids[name] = id
}

func (vmap *masterVNIDMap) unsetVNID(name string) (uint32, bool) {
	id, found := vmap.ids[name]
	delete(vmap.ids, name)
	return id, found
}

func (vmap *masterVNIDMap) getVNIDCount(id uint32) int {
	count := 0
	for _, netid := range vmap.ids {
		if id == netid {
			count = count + 1
		}
	}
	return count
}

func (vmap *masterVNIDMap) isAdminNamespace(nsName string) bool {
	if vmap.adminNamespaces.Has(nsName) {
		return true
	}
	return false
}

func (vmap *masterVNIDMap) populateVNIDs(ctx context.Context, network string, eClient EtcdManager) error {
	netnsList, err := eClient.GetNetNamespaces(ctx, network)
	if err != nil {
		return err
	}

	glog.V(5).Infof("NetNamespaces %v, already exist!", netnsList)

	for _, netns := range netnsList {
		vmap.setVNID(netns.NetName, netns.NetID)

		// Skip GlobalVNID, not part of netID allocation range
		if netns.NetID == vnid.GlobalVNID {
			continue
		}

		switch err := vmap.netIDManager.Allocate(netns.NetID); err {
		case nil: // Expected normal case
		case pnetid.ErrAllocated: // Expected when project networks are joined
		default:
			return fmt.Errorf("unable to allocate netid %d: %v", netns.NetID, err)
		}
	}
	return nil
}

func (vmap *masterVNIDMap) allocateNetID(nsName string) (uint32, bool, error) {
	// Nothing to do if the netid is in the vnid map
	exists := false
	if netid, found := vmap.getVNID(nsName); found {
		glog.V(5).Infof("NetID %v and Namespace %v already exist!", netid, nsName)
		exists = true
		return netid, exists, nil
	}

	// NetNamespace not found, so allocate new NetID
	var netid uint32
	if vmap.isAdminNamespace(nsName) {
		netid = vnid.GlobalVNID
	} else {
		var err error
		netid, err = vmap.netIDManager.AllocateNext()
		if err != nil {
			return 0, exists, err
		}
	}

	vmap.setVNID(nsName, netid)
	glog.Infof("Allocated netid %d for namespace %q", netid, nsName)
	return netid, exists, nil
}

func (vmap *masterVNIDMap) releaseNetID(nsName string) error {
	// Remove NetID from vnid map
	netid, found := vmap.unsetVNID(nsName)
	if !found {
		return fmt.Errorf("netid not found for namespace %q", nsName)
	}

	// Skip vnid.GlobalVNID as it is not part of NetID allocation
	if netid == vnid.GlobalVNID {
		return nil
	}

	// Check if this netid is used by any other namespaces
	// If not, then release the netid
	if count := vmap.getVNIDCount(netid); count == 0 {
		if err := vmap.netIDManager.Release(netid); err != nil {
			return fmt.Errorf("Error while releasing netid %d for namespace %q, %v", netid, nsName, err)
		}
		glog.Infof("Released netid %d for namespace %q", netid, nsName)
	} else {
		glog.V(5).Infof("netid %d for namespace %q is still in use", netid, nsName)
	}
	return nil
}

func (vmap *masterVNIDMap) updateNetID(nsName string, action, args string) (uint32, error) {
	var netid uint32
	allocated := false

	// Check if the given namespace exists or not
	oldnetid, found := vmap.getVNID(nsName)
	if !found {
		return 0, fmt.Errorf("netid not found for namespace %q", nsName)
	}

	// Determine new network ID
	switch action {
	case vnid.GlobalPodNetwork:
		netid = vnid.GlobalVNID
	case vnid.JoinPodNetwork:
		joinNsName := args
		var found bool
		if netid, found = vmap.getVNID(joinNsName); !found {
			return 0, fmt.Errorf("netid not found for namespace %q", joinNsName)
		}
	case vnid.IsolatePodNetwork:
		// Check if the given namespace is already isolated
		if count := vmap.getVNIDCount(oldnetid); count == 1 {
			return oldnetid, nil
		}

		var err error
		netid, err = vmap.netIDManager.AllocateNext()
		if err != nil {
			return 0, err
		}
		allocated = true
	default:
		return 0, fmt.Errorf("invalid pod network action: %v", action)
	}

	// Release old network ID
	if err := vmap.releaseNetID(nsName); err != nil {
		if allocated {
			vmap.netIDManager.Release(netid)
		}
		return 0, err
	}

	// Set new network ID
	vmap.setVNID(nsName, netid)
	glog.Infof("Updated netid %d for namespace %q", netid, nsName)
	return netid, nil
}

// assignVNID, revokeVNID and updateVNID methods updates in-memory structs and persists etcd objects
func (vmap *masterVNIDMap) assignVNID(ctx context.Context, network string, eClient EtcdManager, nsName string) error {
	vmap.lock.Lock()
	defer vmap.lock.Unlock()

	netid, exists, err := vmap.allocateNetID(nsName)
	if err != nil {
		return err
	}

	if !exists {
		glog.Infof("Create NetNamespace for netid:%d, nsName: %q", netid, nsName)
		// Create NetNamespace Object and update vnid map
		netns := &NetNamespace{
			NetName: nsName,
			NetID:   netid,
		}
		err := eClient.AcquireNetNamespace(ctx, network, netns)
		if err != nil {
			vmap.releaseNetID(nsName)
			return err
		}
	} else {
		glog.Infof("Create NetNamespace for netid:%d, nsName: %q", netid, nsName)
	}
	return nil
}

func (vmap *masterVNIDMap) updateVNID(ctx context.Context, network string, eClient EtcdManager, netns *NetNamespace) error {
	vmap.lock.Lock()
	defer vmap.lock.Unlock()

	netid, err := vmap.updateNetID(netns.NetName, netns.Action, netns.Namespace)
	if err != nil {
		return err
	}
	netns.NetID = netid
	netns.Action = ""
	netns.Namespace = ""

	if err := eClient.RenewNetNamespace(ctx, network, netns); err != nil {
		return err
	}
	return nil
}

func (vmap *masterVNIDMap) revokeVNID(ctx context.Context, network string, eClient EtcdManager, nsName string) error {
	vmap.lock.Lock()
	defer vmap.lock.Unlock()

	// Delete NetNamespace object

	if err := eClient.RevokeNetNamespace(ctx, network, nsName); err != nil {
		return err
	}

	if err := vmap.releaseNetID(nsName); err != nil {
		return err
	}
	return nil
}

//--------------------- Master methods ----------------------

func (master *KsdnMaster) VnidStartMaster() error {
	err := master.vnids.populateVNIDs(master.ctx, master.networkInfo.name, master.eClient)
	if err != nil {
		return err
	}

	go utilwait.Forever(master.watchNamespaces, 0)
	go utilwait.Forever(master.watchNetNamespaces, 0)
	return nil
}

func (master *KsdnMaster) watchNamespaces() {
	RunEventQueue(master.kClient, Namespaces, func(delta cache.Delta) error {
		ns := delta.Object.(*kapi.Namespace)
		name := ns.ObjectMeta.Name

		glog.V(5).Infof("Watch %s event for Namespace %q", delta.Type, name)
		switch delta.Type {
		case cache.Sync, cache.Added, cache.Updated:
			if err := master.vnids.assignVNID(master.ctx, master.networkInfo.name, master.eClient, name); err != nil {
				return fmt.Errorf("Error assigning netid: %v", err)
			}
		case cache.Deleted:
			if err := master.vnids.revokeVNID(master.ctx, master.networkInfo.name, master.eClient, name); err != nil {
				return fmt.Errorf("Error revoking netid: %v", err)
			}
		}
		return nil
	})
}

func (master *KsdnMaster) masterHandleNetnsEvent(batch []Event) {
	for _, evt := range batch {
		netns := evt.NetNS
		switch evt.Type {
		case EventAdded:
			if netns.Action == "" {
				glog.V(5).Infof("Null action for netnamespace update")
				continue
			}
			err := master.vnids.updateVNID(master.ctx, master.networkInfo.name, master.eClient, &netns)
			if err != nil {
				glog.Errorf("Error updating netid: %v", err)
			}
		default:
			glog.Errorf("Internal error: unknown event type: ", int(evt.Type))
		}
	}
}

func (master *KsdnMaster) watchNetNamespaces() {
	receiver := make(chan []Event)
	RunNetnsWatch(master.ctx, master.eClient, master.networkInfo.name, receiver, master.masterHandleNetnsEvent)
}
