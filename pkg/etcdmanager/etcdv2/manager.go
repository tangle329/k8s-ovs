// Copyright 2015 flannel authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package etcdv2

import (
	"fmt"
	"strconv"

	etcd "github.com/coreos/etcd/client"
	"github.com/golang/glog"
	"golang.org/x/net/context"

	. "k8s-ovs/pkg/etcdmanager"
)

type Manager struct {
	registry Registry
}

type watchCursor struct {
	index uint64
}

func IsErrEtcdTestFailed(e error) bool {
	if e == nil {
		return false
	}
	etcdErr, ok := e.(etcd.Error)
	return ok && etcdErr.Code == etcd.ErrorCodeTestFailed
}

func IsErrEtcdNodeExist(e error) bool {
	if e == nil {
		return false
	}
	etcdErr, ok := e.(etcd.Error)
	return ok || etcdErr.Code == etcd.ErrorCodeNodeExist
}

func IsErrEtcdKeyNotFound(e error) bool {
	if e == nil {
		return false
	}
	etcdErr, ok := e.(etcd.Error)
	return ok || etcdErr.Code == etcd.ErrorCodeKeyNotFound
}

func (c watchCursor) String() string {
	return strconv.FormatUint(c.index, 10)
}

func NewManager(config *EtcdConfig) (EtcdManager, error) {
	r, err := newEtcdSubnetRegistry(config, nil)
	if err != nil {
		return nil, err
	}
	return newManager(r), nil
}

func newManager(r Registry) EtcdManager {
	return &Manager{
		registry: r,
	}
}

func (m *Manager) GetNetworkConfig(ctx context.Context, network string) (*ClusterNetwork, error) {
	cfg, err := m.registry.getNetworkConfig(ctx, network)
	if err != nil {
		return nil, err
	}

	return ParseClusterNetConfig(cfg)
}

func (m *Manager) AcquireSubnet(ctx context.Context, network string, host string, subnet *HostSubnet) error {
	_, err := m.registry.createSubnet(ctx, network, host, subnet, 0)
	return err
}

func (m *Manager) AcquireNetNamespace(ctx context.Context, network string, netns *NetNamespace) error {
	_, err := m.registry.createNetNamespace(ctx, network, netns.NetName, netns, 0)
	return err
}

func (m *Manager) GetSubnet(ctx context.Context, network string, host string) (*HostSubnet, error) {
	s, _, err := m.registry.getSubnet(ctx, network, host)
	return s, err
}

func (m *Manager) GetNetNamespace(ctx context.Context, network string, namespace string) (*NetNamespace, error) {
	n, _, err := m.registry.getNetNamespace(ctx, network, namespace)
	return n, err
}

func (m *Manager) GetSubnets(ctx context.Context, network string) ([]HostSubnet, error) {
	ss, _, err := m.registry.getSubnets(ctx, network)
	return ss, err
}

func (m *Manager) GetNetNamespaces(ctx context.Context, network string) ([]NetNamespace, error) {
	ns, _, err := m.registry.getNetNamespaces(ctx, network)
	return ns, err
}

func (m *Manager) RenewSubnet(ctx context.Context, network string, subnet *HostSubnet) error {
	_, err := m.registry.updateSubnet(ctx, network, subnet.HostIP, subnet, 0, 0)
	return err
}

func (m *Manager) RenewNetNamespace(ctx context.Context, network string, netns *NetNamespace) error {
	_, err := m.registry.updateNetNamespace(ctx, network, netns.NetName, netns, 0, 0)
	return err
}

func (m *Manager) RevokeSubnet(ctx context.Context, network string, host string) error {
	return m.registry.deleteSubnet(ctx, network, host)
}

func (m *Manager) RevokeNetNamespace(ctx context.Context, network string, namespace string) error {
	return m.registry.deleteNetNamespace(ctx, network, namespace)
}

func getNextIndex(cursor interface{}) (uint64, error) {
	nextIndex := uint64(0)

	if wc, ok := cursor.(watchCursor); ok {
		nextIndex = wc.index
	} else if s, ok := cursor.(string); ok {
		var err error
		nextIndex, err = strconv.ParseUint(s, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("failed to parse cursor: %v", err)
		}
	} else {
		return 0, fmt.Errorf("internal error: watch cursor is of unknown type")
	}

	return nextIndex, nil
}

func (m *Manager) WatchSubnets(ctx context.Context, network string, cursor interface{}) (SubnetWatchResult, error) {
	if cursor == nil {
		return m.subnetsWatchReset(ctx, network)
	}

	nextIndex, err := getNextIndex(cursor)
	if err != nil {
		return SubnetWatchResult{}, err
	}

	evt, index, err := m.registry.watchSubnets(ctx, network, nextIndex)

	switch {
	case err == nil:
		return SubnetWatchResult{
			Events: []Event{evt},
			Cursor: watchCursor{index},
		}, nil

	case isIndexTooSmall(err):
		glog.Warning("Watch of subnets failed because etcd index outside history window")
		return m.subnetsWatchReset(ctx, network)

	default:
		return SubnetWatchResult{}, err
	}
}

func (m *Manager) WatchNetNamespaces(ctx context.Context, network string, cursor interface{}) (NetNamespaceWatchResult, error) {
	if cursor == nil {
		return m.netNamespacesWatchReset(ctx, network)
	}

	nextIndex, err := getNextIndex(cursor)
	if err != nil {
		return NetNamespaceWatchResult{}, err
	}

	evt, index, err := m.registry.watchNetNamespaces(ctx, network, nextIndex)

	switch {
	case err == nil:
		return NetNamespaceWatchResult{
			Events: []Event{evt},
			Cursor: watchCursor{index},
		}, nil

	case isIndexTooSmall(err):
		glog.Warning("Watch of NetNamespaces failed because etcd index outside history window")
		return m.netNamespacesWatchReset(ctx, network)

	default:
		return NetNamespaceWatchResult{}, err
	}
}

func isIndexTooSmall(err error) bool {
	etcdErr, ok := err.(etcd.Error)
	return ok && etcdErr.Code == etcd.ErrorCodeEventIndexCleared
}

// subnetsWatchReset is called when incremental subnet watch failed and we need to grab a snapshot
func (m *Manager) subnetsWatchReset(ctx context.Context, network string) (SubnetWatchResult, error) {
	wr := SubnetWatchResult{}

	subnets, index, err := m.registry.getSubnets(ctx, network)
	if err != nil {
		return wr, fmt.Errorf("failed to retrieve subnet subnets: %v", err)
	}

	wr.Cursor = watchCursor{index}
	wr.Snapshot = subnets
	return wr, nil
}

// netNamespacesWatchReset is called when incremental NetNamespaces watch failed and we need to grab a snapshot
func (m *Manager) netNamespacesWatchReset(ctx context.Context, network string) (NetNamespaceWatchResult, error) {
	wr := NetNamespaceWatchResult{}

	netNSs, index, err := m.registry.getNetNamespaces(ctx, network)
	if err != nil {
		return wr, fmt.Errorf("failed to retrieve NetNamespaces: %v", err)
	}

	wr.Cursor = watchCursor{index}
	wr.Snapshot = netNSs
	return wr, nil
}

// networkWatchReset is called when incremental network watch failed and we need to grab a snapshot
func (m *Manager) networkWatchReset(ctx context.Context) (NetworkWatchResult, error) {
	wr := NetworkWatchResult{}

	networks, index, err := m.registry.getNetworks(ctx)
	if err != nil {
		return wr, fmt.Errorf("failed to retrieve networks: %v", err)
	}

	wr.Cursor = watchCursor{index}
	wr.Snapshot = networks
	return wr, nil
}
