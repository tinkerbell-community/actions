// Package hardware decodes the networking part of a Tinkerbell v1alpha2
// Hardware spec (tinkerbell.org/v1alpha2). Field names and JSON tags follow
// api/v1alpha2/tinkerbell/hardware.go in the Tinkerbell repository; every
// other part of the spec is ignored.
package hardware

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Spec is the subset of HardwareSpec this action reads.
type Spec struct {
	// NetworkInterfaces maps a lower case MAC address to the configuration of
	// the interface that carries it.
	NetworkInterfaces map[string]NetworkInterface `json:"networkInterfaces,omitempty"`
}

// NetworkInterface is the configuration of one network interface.
type NetworkInterface struct {
	IPAM *IPAM `json:"ipam,omitempty"`
}

// IPAM holds the addresses Tinkerbell manages for an interface.
type IPAM struct {
	IPv4 *IP `json:"ipv4,omitempty"`
	IPv6 *IP `json:"ipv6,omitempty"`
}

// IP is the address of one family and the network settings served with it.
type IP struct {
	Address               string                 `json:"address,omitempty"`
	ClasslessStaticRoutes []ClasslessStaticRoute `json:"classlessStaticRoutes,omitempty"`
	DomainSearchList      []string               `json:"domainSearchList,omitempty"`
	Gateway               string                 `json:"gateway,omitempty"`
	Hostname              string                 `json:"hostname,omitempty"`
	Nameservers           []string               `json:"nameservers,omitempty"`
	NTPServers            []string               `json:"ntpServers,omitempty"`
	Prefix                string                 `json:"prefix,omitempty"`
	VLANID                string                 `json:"vlanID,omitempty"`
}

// ClasslessStaticRoute is a route served with DHCP option 121, or alongside
// DHCPv6 for IPv6.
type ClasslessStaticRoute struct {
	DestinationDescriptor string `json:"destinationDescriptor"`
	Router                string `json:"router"`
}

// Parse decodes a v1alpha2 Hardware spec, or any JSON object holding its
// networkInterfaces, and lower cases the MAC addresses.
func Parse(raw []byte) (*Spec, error) {
	var probe struct {
		Spec
		// Interfaces is the v1alpha1 list, decoded only to recognise it.
		Interfaces json.RawMessage `json:"interfaces"`
	}

	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("decoding Hardware spec: %w", err)
	}

	if len(probe.NetworkInterfaces) == 0 {
		if len(probe.Interfaces) > 0 {
			return nil, errors.New("the Hardware spec has the v1alpha1 interfaces list; talosmeta reads the v1alpha2 networkInterfaces map")
		}

		return nil, errors.New("the Hardware spec has no networkInterfaces")
	}

	spec := &Spec{NetworkInterfaces: make(map[string]NetworkInterface, len(probe.NetworkInterfaces))}

	for mac, iface := range probe.NetworkInterfaces {
		key := strings.ToLower(strings.TrimSpace(mac))
		if _, duplicate := spec.NetworkInterfaces[key]; duplicate {
			return nil, fmt.Errorf("networkInterfaces lists MAC %s more than once", key)
		}

		spec.NetworkInterfaces[key] = iface
	}

	return spec, nil
}
