package etcdmanager

import (
	"golang.org/x/net/context"
	"time"
)

type ClusterNetwork struct {
	Name             string
	Network          string
	HostSubnetLength uint32
	ServiceNetwork   string
	PluginName       string
}

// HostSubnet encapsulates the inputs needed to define the container subnet network on a node
type HostSubnet struct {
	// host may just be an IP address, resolvable hostname or a complete DNS
	Host   string
	HostIP string
	Subnet string
	Assign bool
}

// NetNamespace holds the network id against its name
type NetNamespace struct {
	NetName   string
	NetID     uint32
	Action    string
	Namespace string
}

type (
	EventType int

	Event struct {
		Type    EventType    `json:"type"`
		Subnet  HostSubnet   `json:"hostsubnet,omitempty"`
		Network string       `json:"network,omitempty"`
		NetNS   NetNamespace `json:"netnamespace,omitempty"`
	}
)

const (
	EventAdded EventType = iota
	EventRemoved
)

type SubnetWatchResult struct {
	// Either Events or Snapshot will be set.  If Events is empty, it means
	// the cursor was out of range and Snapshot contains the current list
	// of items, even if empty.
	Events   []Event      `json:"events"`
	Snapshot []HostSubnet `json:"snapshot"`
	Cursor   interface{}  `json:"cursor"`
}

type NetNamespaceWatchResult struct {
	// Either Events or Snapshot will be set.  If Events is empty, it means
	// the cursor was out of range and Snapshot contains the current list
	// of items, even if empty.
	Events   []Event        `json:"events"`
	Snapshot []NetNamespace `json:"snapshot"`
	Cursor   interface{}    `json:"cursor"`
}

type NetworkWatchResult struct {
	// Either Events or Snapshot will be set.  If Events is empty, it means
	// the cursor was out of range and Snapshot contains the current list
	// of items, even if empty.
	Events   []Event     `json:"events"`
	Snapshot []string    `json:"snapshot"`
	Cursor   interface{} `json:"cursor,omitempty"`
}

type Lease struct {
	Host       string
	Attrs      HostSubnet
	Expiration time.Time

	Asof uint64
}

type EtcdManager interface {
	GetNetworkConfig(ctx context.Context, network string) (*ClusterNetwork, error)
	AcquireSubnet(ctx context.Context, network string, host string, subnet *HostSubnet) error
	GetSubnet(ctx context.Context, network string, host string) (*HostSubnet, error)
	GetNetNamespace(ctx context.Context, network string, namespace string) (*NetNamespace, error)
	GetSubnets(ctx context.Context, network string) ([]HostSubnet, error)
	GetNetNamespaces(ctx context.Context, network string) ([]NetNamespace, error)
	AcquireNetNamespace(ctx context.Context, network string, attrs *NetNamespace) error
	RenewSubnet(ctx context.Context, network string, subnet *HostSubnet) error
	RenewNetNamespace(ctx context.Context, network string, netns *NetNamespace) error
	RevokeSubnet(ctx context.Context, network string, host string) error
	RevokeNetNamespace(ctx context.Context, network string, namespace string) error
	WatchSubnets(ctx context.Context, network string, cursor interface{}) (SubnetWatchResult, error)
	WatchNetNamespaces(ctx context.Context, network string, cursor interface{}) (NetNamespaceWatchResult, error)
}
