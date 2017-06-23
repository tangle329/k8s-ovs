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
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sync"
	"time"

	etcd "github.com/coreos/etcd/client"
	"github.com/coreos/etcd/pkg/transport"
	"github.com/golang/glog"
	"golang.org/x/net/context"

	. "k8s-ovs/pkg/etcdmanager"
)

var (
	errTryAgain = errors.New("try again")
)

type Registry interface {
	getNetworkConfig(ctx context.Context, network string) (string, error)
	getSubnets(ctx context.Context, network string) ([]HostSubnet, uint64, error)
	getNetNamespaces(ctx context.Context, network string) ([]NetNamespace, uint64, error)
	getSubnet(ctx context.Context, network string, host string) (*HostSubnet, uint64, error)
	getNetNamespace(ctx context.Context, network string, namespace string) (*NetNamespace, uint64, error)
	createSubnet(ctx context.Context, network string, host string, attrs *HostSubnet, ttl time.Duration) (time.Time, error)
	createNetNamespace(ctx context.Context, network string, namespace string, attrs *NetNamespace, ttl time.Duration) (time.Time, error)
	updateSubnet(ctx context.Context, network string, host string, attrs *HostSubnet, ttl time.Duration, asof uint64) (time.Time, error)
	updateNetNamespace(ctx context.Context, network string, namespace string, attrs *NetNamespace, ttl time.Duration, asof uint64) (time.Time, error)
	deleteSubnet(ctx context.Context, network string, host string) error
	deleteNetNamespace(ctx context.Context, network string, namespace string) error
	watchSubnets(ctx context.Context, network string, since uint64) (Event, uint64, error)
	watchNetNamespaces(ctx context.Context, network string, since uint64) (Event, uint64, error)
	getNetworks(ctx context.Context) ([]string, uint64, error)
	//	watchNetworks(ctx context.Context, since uint64) (Event, uint64, error)
}

type EtcdConfig struct {
	Endpoints []string
	Keyfile   string
	Certfile  string
	CAFile    string
	Prefix    string
	Username  string
	Password  string
}

type etcdNewFunc func(c *EtcdConfig) (etcd.KeysAPI, error)

type etcdSubnetRegistry struct {
	cliNewFunc   etcdNewFunc
	mux          sync.Mutex
	cli          etcd.KeysAPI
	etcdCfg      *EtcdConfig
	networkRegex *regexp.Regexp
}

func newEtcdClient(c *EtcdConfig) (etcd.KeysAPI, error) {
	tlsInfo := transport.TLSInfo{
		CertFile: c.Certfile,
		KeyFile:  c.Keyfile,
		CAFile:   c.CAFile,
	}

	t, err := transport.NewTransport(tlsInfo, time.Second)
	if err != nil {
		return nil, err
	}

	cli, err := etcd.New(etcd.Config{
		Endpoints: c.Endpoints,
		Transport: t,
		Username:  c.Username,
		Password:  c.Password,
	})
	if err != nil {
		return nil, err
	}

	return etcd.NewKeysAPI(cli), nil
}

func newEtcdSubnetRegistry(config *EtcdConfig, cliNewFunc etcdNewFunc) (Registry, error) {
	r := &etcdSubnetRegistry{
		etcdCfg:      config,
		networkRegex: regexp.MustCompile(config.Prefix + `/([^/]*)(/|/config)?$`),
	}
	if cliNewFunc != nil {
		r.cliNewFunc = cliNewFunc
	} else {
		r.cliNewFunc = newEtcdClient
	}

	var err error
	r.cli, err = r.cliNewFunc(config)
	if err != nil {
		return nil, err
	}

	return r, nil
}

func (esr *etcdSubnetRegistry) getNetworkConfig(ctx context.Context, network string) (string, error) {
	key := path.Join(esr.etcdCfg.Prefix, network, "config")
	resp, err := esr.client().Get(ctx, key, &etcd.GetOptions{Quorum: true})
	if err != nil {
		return "", err
	}
	return resp.Node.Value, nil
}

// GetSubnets queries etcd to get a list of currently allocated subnets for a given network.
// It returns the subnets along with the "as-of" etcd-index that can be used as the starting
// point for etcd watch.
func (esr *etcdSubnetRegistry) getSubnets(ctx context.Context, network string) ([]HostSubnet, uint64, error) {
	key := path.Join(esr.etcdCfg.Prefix, network, "subnets")
	resp, err := esr.client().Get(ctx, key, &etcd.GetOptions{Recursive: true, Quorum: true})
	if err != nil {
		if etcdErr, ok := err.(etcd.Error); ok && etcdErr.Code == etcd.ErrorCodeKeyNotFound {
			// key not found: treat it as empty set
			return []HostSubnet{}, etcdErr.Index, nil
		}
		return nil, 0, err
	}

	subnets := []HostSubnet{}
	for _, node := range resp.Node.Nodes {
		s, err := nodeToSubnet(node)
		if err != nil {
			glog.Warningf("Ignoring bad subnet node: %v", err)
			continue
		}

		subnets = append(subnets, *s)
	}

	return subnets, resp.Index, nil
}

// GetNetNamespaces queries etcd to get a list of currently allocated NetNamespaces.
// It returns the subnets along with the "as-of" etcd-index that can be used as the starting
// point for etcd watch.
func (esr *etcdSubnetRegistry) getNetNamespaces(ctx context.Context, network string) ([]NetNamespace, uint64, error) {
	key := path.Join(esr.etcdCfg.Prefix, network, "netnamespaces")
	resp, err := esr.client().Get(ctx, key, &etcd.GetOptions{Recursive: true, Quorum: true})
	if err != nil {
		if etcdErr, ok := err.(etcd.Error); ok && etcdErr.Code == etcd.ErrorCodeKeyNotFound {
			// key not found: treat it as empty set
			return []NetNamespace{}, etcdErr.Index, nil
		}
		return nil, 0, err
	}

	netNSs := []NetNamespace{}
	for _, node := range resp.Node.Nodes {
		netNS := &NetNamespace{}
		if err := json.Unmarshal([]byte(node.Value), netNS); err != nil {
			glog.Warningf("Ignoring bad netnamespace node: %v", err)
			continue
		}
		netNSs = append(netNSs, *netNS)
	}

	return netNSs, resp.Index, nil
}

func (esr *etcdSubnetRegistry) getSubnet(ctx context.Context, network string, host string) (*HostSubnet, uint64, error) {
	key := path.Join(esr.etcdCfg.Prefix, network, "subnets", host)
	resp, err := esr.client().Get(ctx, key, &etcd.GetOptions{Quorum: true})
	if err != nil {
		return nil, 0, err
	}

	s, err := nodeToSubnet(resp.Node)
	return s, resp.Index, err
}

func (esr *etcdSubnetRegistry) getNetNamespace(ctx context.Context, network string, namespace string) (*NetNamespace, uint64, error) {
	key := path.Join(esr.etcdCfg.Prefix, network, "netnamespaces", namespace)
	resp, err := esr.client().Get(ctx, key, &etcd.GetOptions{Quorum: true})
	if err != nil {
		return nil, 0, err
	}

	netNS := &NetNamespace{}
	if err := json.Unmarshal([]byte(resp.Node.Value), netNS); err != nil {
		return nil, 0, err
	}

	return netNS, resp.Index, err
}

func (esr *etcdSubnetRegistry) createSubnet(ctx context.Context, network string, host string, subnet *HostSubnet, ttl time.Duration) (time.Time, error) {
	key := path.Join(esr.etcdCfg.Prefix, network, "subnets", host)
	value, err := json.Marshal(subnet)
	if err != nil {
		return time.Time{}, err
	}

	opts := &etcd.SetOptions{
		PrevExist: etcd.PrevNoExist,
		TTL:       ttl,
	}

	resp, err := esr.client().Set(ctx, key, string(value), opts)
	if err != nil {
		return time.Time{}, err
	}

	exp := time.Time{}
	if resp.Node.Expiration != nil {
		exp = *resp.Node.Expiration
	}

	return exp, nil
}

func (esr *etcdSubnetRegistry) createNetNamespace(ctx context.Context, network string, namespace string, netns *NetNamespace, ttl time.Duration) (time.Time, error) {
	key := path.Join(esr.etcdCfg.Prefix, network, "netnamespaces", namespace)
	value, err := json.Marshal(netns)
	if err != nil {
		return time.Time{}, err
	}

	opts := &etcd.SetOptions{
		PrevExist: etcd.PrevNoExist,
		TTL:       ttl,
	}

	resp, err := esr.client().Set(ctx, key, string(value), opts)
	if err != nil {
		return time.Time{}, err
	}

	exp := time.Time{}
	if resp.Node.Expiration != nil {
		exp = *resp.Node.Expiration
	}

	return exp, nil
}

func (esr *etcdSubnetRegistry) updateSubnet(ctx context.Context, network string, host string, subnet *HostSubnet, ttl time.Duration, asof uint64) (time.Time, error) {
	key := path.Join(esr.etcdCfg.Prefix, network, "subnets", host)
	value, err := json.Marshal(subnet)
	if err != nil {
		return time.Time{}, err
	}

	resp, err := esr.client().Set(ctx, key, string(value), &etcd.SetOptions{
		PrevIndex: asof,
		TTL:       ttl,
	})
	if err != nil {
		return time.Time{}, err
	}

	exp := time.Time{}
	if resp.Node.Expiration != nil {
		exp = *resp.Node.Expiration
	}

	return exp, nil
}

func (esr *etcdSubnetRegistry) updateNetNamespace(ctx context.Context, network string, namespace string, netns *NetNamespace, ttl time.Duration, asof uint64) (time.Time, error) {
	key := path.Join(esr.etcdCfg.Prefix, network, "netnamespaces", namespace)
	value, err := json.Marshal(netns)
	if err != nil {
		return time.Time{}, err
	}

	resp, err := esr.client().Set(ctx, key, string(value), &etcd.SetOptions{
		PrevIndex: asof,
		TTL:       ttl,
	})
	if err != nil {
		return time.Time{}, err
	}

	exp := time.Time{}
	if resp.Node.Expiration != nil {
		exp = *resp.Node.Expiration
	}

	return exp, nil
}

func (esr *etcdSubnetRegistry) deleteSubnet(ctx context.Context, network string, host string) error {
	key := path.Join(esr.etcdCfg.Prefix, network, "subnets", host)
	_, err := esr.client().Delete(ctx, key, nil)
	return err
}

func (esr *etcdSubnetRegistry) deleteNetNamespace(ctx context.Context, network string, namespace string) error {
	key := path.Join(esr.etcdCfg.Prefix, network, "netnamespaces", namespace)
	_, err := esr.client().Delete(ctx, key, nil)
	return err
}

func (esr *etcdSubnetRegistry) watchSubnets(ctx context.Context, network string, since uint64) (Event, uint64, error) {
	key := path.Join(esr.etcdCfg.Prefix, network, "subnets")
	opts := &etcd.WatcherOptions{
		AfterIndex: since,
		Recursive:  true,
	}
	e, err := esr.client().Watcher(key, opts).Next(ctx)
	if err != nil {
		return Event{}, 0, err
	}

	evt, err := parseSubnetWatchResponse(e)
	return evt, e.Node.ModifiedIndex, err
}

func (esr *etcdSubnetRegistry) watchNetNamespaces(ctx context.Context, network string, since uint64) (Event, uint64, error) {
	key := path.Join(esr.etcdCfg.Prefix, network, "netnamespaces")
	opts := &etcd.WatcherOptions{
		AfterIndex: since,
		Recursive:  true,
	}
	e, err := esr.client().Watcher(key, opts).Next(ctx)
	if err != nil {
		return Event{}, 0, err
	}

	evt, err := parseNetNamespaceWatchResponse(e)
	return evt, e.Node.ModifiedIndex, err
}

// GetNetworks queries etcd to get a list of network names.  It returns the
// networks along with the 'as-of' etcd-index that can be used as the starting
// point for etcd watch.
func (esr *etcdSubnetRegistry) getNetworks(ctx context.Context) ([]string, uint64, error) {
	resp, err := esr.client().Get(ctx, esr.etcdCfg.Prefix, &etcd.GetOptions{Recursive: true, Quorum: true})

	networks := []string{}

	if err == nil {
		for _, node := range resp.Node.Nodes {
			// Look for '/config' on the child nodes
			for _, child := range node.Nodes {
				netname, isConfig := esr.parseNetworkKey(child.Key)
				if isConfig {
					networks = append(networks, netname)
				}
			}
		}

		return networks, resp.Index, nil
	}

	if etcdErr, ok := err.(etcd.Error); ok && etcdErr.Code == etcd.ErrorCodeKeyNotFound {
		// key not found: treat it as empty set
		return networks, etcdErr.Index, nil
	}

	return nil, 0, err
}

/*
func (esr *etcdSubnetRegistry) watchNetworks(ctx context.Context, since uint64) (Event, uint64, error) {
	key := esr.etcdCfg.Prefix
	opts := &etcd.WatcherOptions{
		AfterIndex: since,
		Recursive:  true,
	}
	e, err := esr.client().Watcher(key, opts).Next(ctx)
	if err != nil {
		return Event{}, 0, err
	}

	return esr.parseNetworkWatchResponse(e)
}
*/
func (esr *etcdSubnetRegistry) client() etcd.KeysAPI {
	esr.mux.Lock()
	defer esr.mux.Unlock()
	return esr.cli
}

func (esr *etcdSubnetRegistry) resetClient() {
	esr.mux.Lock()
	defer esr.mux.Unlock()

	var err error
	esr.cli, err = newEtcdClient(esr.etcdCfg)
	if err != nil {
		panic(fmt.Errorf("resetClient: error recreating etcd client: %v", err))
	}
}

func parseSubnetWatchResponse(resp *etcd.Response) (Event, error) {
	switch resp.Action {
	case "delete", "expire":
		return Event{
			EventRemoved,
			HostSubnet{Host: resp.Node.Key},
			"",
			NetNamespace{},
		}, nil

	default:
		subnet := &HostSubnet{}
		err := json.Unmarshal([]byte(resp.Node.Value), subnet)
		if err != nil {
			return Event{}, err
		}

		evt := Event{
			EventAdded,
			*subnet,
			"",
			NetNamespace{},
		}
		return evt, nil
	}
}

func parseNetNamespaceWatchResponse(resp *etcd.Response) (Event, error) {
	switch resp.Action {
	case "delete", "expire":
		return Event{
			EventRemoved,
			HostSubnet{},
			"",
			NetNamespace{NetName: resp.Node.Key},
		}, nil

	default:
		netns := &NetNamespace{}
		err := json.Unmarshal([]byte(resp.Node.Value), netns)
		if err != nil {
			return Event{}, err
		}

		evt := Event{
			EventAdded,
			HostSubnet{},
			"",
			*netns,
		}
		return evt, nil
	}
}

func (esr *etcdSubnetRegistry) parseNetworkWatchResponse(resp *etcd.Response) (Event, uint64, error) {
	index := resp.Node.ModifiedIndex
	netname, isConfig := esr.parseNetworkKey(resp.Node.Key)
	if netname == "" {
		return Event{}, index, errTryAgain
	}

	var evt Event

	switch resp.Action {
	case "delete":
		evt = Event{
			EventRemoved,
			HostSubnet{},
			netname,
			NetNamespace{},
		}

	default:
		if !isConfig {
			// Ignore non .../<netname>/config keys; tell caller to try again
			return Event{}, index, errTryAgain
		}

		_, err := ParseClusterNetConfig(resp.Node.Value)
		if err != nil {
			return Event{}, index, err
		}

		evt = Event{
			EventAdded,
			HostSubnet{},
			netname,
			NetNamespace{},
		}
	}

	return evt, index, nil
}

// Returns network name from config key (eg, /coreos.com/network/foobar/config),
// if the 'config' key isn't present we don't consider the network valid
func (esr *etcdSubnetRegistry) parseNetworkKey(s string) (string, bool) {
	if parts := esr.networkRegex.FindStringSubmatch(s); len(parts) == 3 {
		return parts[1], parts[2] != ""
	}

	return "", false
}

func nodeToSubnet(node *etcd.Node) (*HostSubnet, error) {
	subnet := &HostSubnet{}
	if err := json.Unmarshal([]byte(node.Value), subnet); err != nil {
		return nil, err
	}

	return subnet, nil
}

func ParseClusterNetConfig(s string) (*ClusterNetwork, error) {
	cfg := new(ClusterNetwork)
	err := json.Unmarshal([]byte(s), cfg)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}
