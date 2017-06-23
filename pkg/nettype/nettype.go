package nettype

import (
	"strings"
)

const (
	SingleTenantPluginName = "k8s-ovs-subnet"
	MultiTenantPluginName  = "k8s-ovs-multitenant"
)

func IsKovsNetworkPlugin(pluginName string) bool {
	switch strings.ToLower(pluginName) {
	case SingleTenantPluginName, MultiTenantPluginName:
		return true
	}
	return false
}

func IsKovsCloudMultitenantNetworkPlugin(pluginName string) bool {
	if strings.ToLower(pluginName) == MultiTenantPluginName {
		return true
	}
	return false
}
