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

package etcdmanager

import (
	"sync"
	"time"

	"github.com/golang/glog"
	"golang.org/x/net/context"
)

// WatchSubnets performs a long term watch of the given network's subnets
// and communicates addition/deletion events on receiver channel. It takes care
// of handling "fall-behind" logic where the history window has advanced too far
// and it needs to diff the latest snapshot with its saved state and generate events
func WatchSubnets(ctx context.Context, sm EtcdManager, network string, receiver chan []Event) {
	sw := &subnetWatcher{}
	var cursor interface{}

	for {
		res, err := sm.WatchSubnets(ctx, network, cursor)
		if err != nil {
			if err == context.Canceled || err == context.DeadlineExceeded {
				return
			}

			glog.Errorf("Watch subnets: %v", err)
			time.Sleep(time.Second)
			continue
		}

		cursor = res.Cursor

		var batch []Event

		if len(res.Events) > 0 {
			batch = sw.update(res.Events)
		} else {
			batch = sw.reset(res.Snapshot)
		}

		if len(batch) > 0 {
			receiver <- batch
		}
	}
}

type subnetWatcher struct {
	subnets []HostSubnet
}

func (sw *subnetWatcher) reset(subnets []HostSubnet) []Event {
	batch := []Event{}

	for _, ns := range subnets {
		found := false
		for i, os := range sw.subnets {
			if os.Host == ns.Host {
				sw.subnets = deleteSubnet(sw.subnets, i)
				found = true
				break
			}
		}

		if !found {
			// new subnet
			batch = append(batch, Event{EventAdded, ns, "", NetNamespace{}})
		}
	}

	// everything left in sm.subnets has been deleted
	for _, s := range sw.subnets {
		batch = append(batch, Event{EventRemoved, s, "", NetNamespace{}})
	}

	// copy the subnets over (caution: don't just assign a slice)
	sw.subnets = make([]HostSubnet, len(subnets))
	copy(sw.subnets, subnets)

	return batch
}

func (sw *subnetWatcher) update(events []Event) []Event {
	batch := []Event{}

	for _, e := range events {
		switch e.Type {
		case EventAdded:
			batch = append(batch, sw.add(&e.Subnet))

		case EventRemoved:
			batch = append(batch, sw.remove(&e.Subnet))
		}
	}

	return batch
}

func (sw *subnetWatcher) add(subnet *HostSubnet) Event {
	for i, s := range sw.subnets {
		if s.Host == subnet.Host {
			sw.subnets[i] = *subnet
			return Event{EventAdded, *subnet, "", NetNamespace{}}
		}
	}

	sw.subnets = append(sw.subnets, *subnet)

	return Event{EventAdded, sw.subnets[len(sw.subnets)-1], "", NetNamespace{}}
}

func (sw *subnetWatcher) remove(subnet *HostSubnet) Event {
	for i, s := range sw.subnets {
		if s.Host == subnet.Host {
			sw.subnets = deleteSubnet(sw.subnets, i)
			return Event{EventRemoved, s, "", NetNamespace{}}
		}
	}

	glog.Errorf("Removed subnet (%s) was not found", subnet.Host)
	return Event{EventRemoved, *subnet, "", NetNamespace{}}
}

func deleteSubnet(s []HostSubnet, i int) []HostSubnet {
	s[i] = s[len(s)-1]
	return s[:len(s)-1]
}

func RunSubnetWatch(ctx context.Context, sm EtcdManager, network string, receiver chan []Event, handle func(batch []Event)) {
	wg := sync.WaitGroup{}

	glog.Info("RunSubnetWatch started.")
	wg.Add(1)
	go func() {
		WatchSubnets(ctx, sm, network, receiver)
		wg.Done()
	}()

	defer wg.Wait()
	defer glog.Info("RunSubnetWatch exited.")

	for {
		select {
		case evtBatch := <-receiver:
			handle(evtBatch)

		case <-ctx.Done():
			return
		}
	}
}

// WatchNetNamespaces performs a long term watch of the given network's netnamespaces
// and communicates addition/deletion events on receiver channel. It takes care
// of handling "fall-behind" logic where the history window has advanced too far
// and it needs to diff the latest snapshot with its saved state and generate events
func WatchNetNamespaces(ctx context.Context, sm EtcdManager, network string, receiver chan []Event) {
	nw := &netnamespaceWatcher{}
	var cursor interface{}

	for {
		res, err := sm.WatchNetNamespaces(ctx, network, cursor)
		if err != nil {
			if err == context.Canceled || err == context.DeadlineExceeded {
				return
			}

			glog.Errorf("Watch NetNamespaces: %v", err)
			time.Sleep(time.Second)
			continue
		}

		cursor = res.Cursor

		var batch []Event

		if len(res.Events) > 0 {
			batch = nw.update(res.Events)
		} else {
			batch = nw.reset(res.Snapshot)
		}

		if len(batch) > 0 {
			receiver <- batch
		}
	}
}

type netnamespaceWatcher struct {
	netnss []NetNamespace
}

func (nw *netnamespaceWatcher) reset(netnss []NetNamespace) []Event {
	batch := []Event{}

	for _, ns := range netnss {
		found := false
		for i, on := range nw.netnss {
			if on.NetName == ns.NetName {
				nw.netnss = deleteNetNS(nw.netnss, i)
				found = true
				break
			}
		}

		if !found {
			// new subnet
			batch = append(batch, Event{EventAdded, HostSubnet{}, "", ns})
		}
	}

	// everything left in sm.subnets has been deleted
	for _, s := range nw.netnss {
		batch = append(batch, Event{EventRemoved, HostSubnet{}, "", s})
	}

	// copy the subnets over (caution: don't just assign a slice)
	nw.netnss = make([]NetNamespace, len(netnss))
	copy(nw.netnss, netnss)

	return batch
}

func (nw *netnamespaceWatcher) update(events []Event) []Event {
	batch := []Event{}

	for _, e := range events {
		switch e.Type {
		case EventAdded:
			batch = append(batch, nw.add(&e.NetNS))

		case EventRemoved:
			batch = append(batch, nw.remove(&e.NetNS))
		}
	}

	return batch
}

func (nw *netnamespaceWatcher) add(netns *NetNamespace) Event {
	for i, n := range nw.netnss {
		if n.NetName == netns.NetName {
			nw.netnss[i] = *netns
			return Event{EventAdded, HostSubnet{}, "", *netns}
		}
	}
	nw.netnss = append(nw.netnss, *netns)

	return Event{EventAdded, HostSubnet{}, "", nw.netnss[len(nw.netnss)-1]}
}

func (nw *netnamespaceWatcher) remove(netns *NetNamespace) Event {
	for i, n := range nw.netnss {
		if n.NetName == netns.NetName {
			nw.netnss = deleteNetNS(nw.netnss, i)
			return Event{EventRemoved, HostSubnet{}, "", n}
		}
	}

	glog.Errorf("Removed netns (%s) was not found", netns.NetName)
	return Event{EventRemoved, HostSubnet{}, "", *netns}
}

func deleteNetNS(n []NetNamespace, i int) []NetNamespace {
	n[i] = n[len(n)-1]
	return n[:len(n)-1]
}

func RunNetnsWatch(ctx context.Context, sm EtcdManager, network string, receiver chan []Event, handle func(batch []Event)) {
	wg := sync.WaitGroup{}

	glog.Info("Watching for new NetNamespaces")
	wg.Add(1)
	go func() {
		WatchNetNamespaces(ctx, sm, network, receiver)
		wg.Done()
	}()

	defer wg.Wait()

	for {
		select {
		case evtBatch := <-receiver:
			handle(evtBatch)

		case <-ctx.Done():
			return
		}
	}
}
