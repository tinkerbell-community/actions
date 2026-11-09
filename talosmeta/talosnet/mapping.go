package talosnet

import (
	"cmp"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/tinkerbell/actions/talosmeta/hardware"
)

// Namer resolves the interface name Talos will use for a MAC address.
type Namer interface {
	Name(mac string) (string, error)
}

// Options control how Hardware data is mapped.
type Options struct {
	// Namer resolves the link name of each MAC address.
	Namer Namer
	// Bond, when set, enslaves every interface of the Hardware to one bond
	// that carries the addresses. Otherwise each interface with an address is
	// configured on its own.
	Bond *Bond
}

// nic is one Hardware interface and the link Talos knows it by.
type nic struct {
	mac  string
	link string
	ipam *hardware.IPAM
}

// FromHardware maps a Tinkerbell v1alpha2 Hardware spec onto a Talos platform
// network configuration. Every address in networkInterfaces[].ipam becomes an
// address on its interface, or on the bond, with a default route through its
// gateway and its classless static routes. Hostname, resolvers and time
// servers are collected once for the machine.
func FromHardware(spec *hardware.Spec, opts Options) (*Config, error) {
	if spec == nil || len(spec.NetworkInterfaces) == 0 {
		return nil, errors.New("the hardware data has no networkInterfaces")
	}

	if opts.Namer == nil {
		return nil, errors.New("no link namer configured")
	}

	if opts.Bond != nil {
		if err := opts.Bond.Validate(); err != nil {
			return nil, err
		}
	}

	nics, err := resolveNICs(spec, opts)
	if err != nil {
		return nil, err
	}

	cfg := newConfig()

	if opts.Bond != nil {
		err = addBond(cfg, nics, opts.Bond)
	} else {
		err = addInterfaces(cfg, nics)
	}

	if err != nil {
		return nil, err
	}

	if len(cfg.Addresses) == 0 {
		return nil, errors.New("no interface has a static address (networkInterfaces[].ipam.ipv4.address or ipv6.address)")
	}

	addMachineSettings(cfg, nics)

	return cfg, nil
}

// resolveNICs names the interfaces the document configures, in link order.
// A bond enslaves all of them; otherwise only interfaces with an address are
// configured, so the others need not exist on the machine.
func resolveNICs(spec *hardware.Spec, opts Options) ([]nic, error) {
	macs := make([]string, 0, len(spec.NetworkInterfaces))

	for mac, iface := range spec.NetworkInterfaces {
		if opts.Bond != nil || hasAddress(iface.IPAM) {
			macs = append(macs, mac)
		}
	}

	slices.Sort(macs)

	nics := make([]nic, 0, len(macs))
	owners := make(map[string]string, len(macs))

	for _, mac := range macs {
		link, err := opts.Namer.Name(mac)
		if err != nil {
			return nil, fmt.Errorf("interface %s: resolving link name: %w", mac, err)
		}

		if other, ok := owners[link]; ok {
			return nil, fmt.Errorf("interfaces %s and %s both resolve to link %s", other, mac, link)
		}

		owners[link] = mac
		nics = append(nics, nic{mac: mac, link: link, ipam: spec.NetworkInterfaces[mac].IPAM})
	}

	slices.SortFunc(nics, func(a, b nic) int { return compareLinkNames(a.link, b.link) })

	return nics, nil
}

func addInterfaces(cfg *Config, nics []nic) error {
	for _, n := range nics {
		cfg.Links = append(cfg.Links, Link{Name: n.link, Up: true, Layer: layerPlatform})

		if err := addIPAM(cfg, n.link, n.ipam); err != nil {
			return fmt.Errorf("interface %s (%s): %w", n.mac, n.link, err)
		}
	}

	return nil
}

// addBond creates the bond, enslaves every interface in link order and puts
// the addresses on the bond. The first slave is the bond's primary MAC source,
// so link order decides which MAC the bond answers with.
func addBond(cfg *Config, nics []nic, bond *Bond) error {
	cfg.Links = append(cfg.Links, bond.link())

	for i, n := range nics {
		if n.link == bond.Name {
			return fmt.Errorf("interface %s resolves to link %s, the name of the bond", n.mac, n.link)
		}

		cfg.Links = append(cfg.Links, Link{
			Name:       n.link,
			Up:         true,
			MasterName: bond.Name,
			SlaveIndex: i,
			Layer:      layerPlatform,
		})
	}

	ipam, err := bondIPAM(nics)
	if err != nil {
		return fmt.Errorf("bond %s: %w", bond.Name, err)
	}

	if err := addIPAM(cfg, bond.Name, ipam); err != nil {
		return fmt.Errorf("bond %s: %w", bond.Name, err)
	}

	return nil
}

// bondIPAM picks, per address family, the configuration of the first
// interface with an address. Every other interface must either have no
// address of that family or the same one: a bond has a single identity.
func bondIPAM(nics []nic) (*hardware.IPAM, error) {
	merged := &hardware.IPAM{}

	pick := func(family string, chosen **hardware.IP, from nic, ip *hardware.IP) error {
		if ip == nil || ip.Address == "" {
			return nil
		}

		if *chosen == nil {
			*chosen = ip

			return nil
		}

		if !sameAddress((*chosen).Address, ip.Address) {
			return fmt.Errorf("the bonded interfaces have different %s addresses, %s and %s (%s)", family, (*chosen).Address, ip.Address, from.mac)
		}

		return nil
	}

	for _, n := range nics {
		if n.ipam == nil {
			continue
		}

		if err := pick("IPv4", &merged.IPv4, n, n.ipam.IPv4); err != nil {
			return nil, err
		}

		if err := pick("IPv6", &merged.IPv6, n, n.ipam.IPv6); err != nil {
			return nil, err
		}
	}

	return merged, nil
}

// addIPAM configures the addresses of ipam on link, or on a VLAN link on top
// of it when the address has a VLAN ID.
func addIPAM(cfg *Config, link string, ipam *hardware.IPAM) error {
	if ipam == nil {
		return nil
	}

	for _, f := range []struct {
		name   string
		number int64
		ip     *hardware.IP
	}{{"ipv4", 4, ipam.IPv4}, {"ipv6", 6, ipam.IPv6}} {
		if f.ip == nil || f.ip.Address == "" {
			continue
		}

		if err := addAddress(cfg, link, f.ip, f.number); err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
	}

	return nil
}

func addAddress(cfg *Config, link string, ip *hardware.IP, family int64) error {
	addr, err := netip.ParseAddr(ip.Address)
	if err != nil {
		return fmt.Errorf("parsing address %q: %w", ip.Address, err)
	}

	addr = addr.Unmap()
	if familyNumber(addr) != family {
		return fmt.Errorf("address %s is not an IPv%d address", addr, family)
	}

	prefixLen, err := PrefixLength(ip.Prefix, family)
	if err != nil {
		return fmt.Errorf("prefix %q: %w", ip.Prefix, err)
	}

	attachTo, err := vlanLink(cfg, link, ip.VLANID)
	if err != nil {
		return err
	}

	fam := familyOf(addr)

	cfg.Addresses = append(cfg.Addresses, Address{
		Address:  netip.PrefixFrom(addr, prefixLen).String(),
		LinkName: attachTo,
		Family:   fam,
		Scope:    scopeGlobal,
		Flags:    flagsPermanent,
		Layer:    layerPlatform,
	})

	if ip.Gateway != "" {
		gw, err := netip.ParseAddr(ip.Gateway)
		if err != nil {
			return fmt.Errorf("parsing gateway %q: %w", ip.Gateway, err)
		}

		cfg.Routes = append(cfg.Routes, Route{
			Family:      fam,
			Gateway:     gw.Unmap().String(),
			OutLinkName: attachTo,
			Table:       tableMain,
			Priority:    defaultRoutePriority(addr),
			Scope:       scopeGlobal,
			Type:        routeUnicast,
			Protocol:    protocolStatic,
			Layer:       layerPlatform,
		})
	}

	for _, r := range ip.ClasslessStaticRoutes {
		dst, err := netip.ParsePrefix(r.DestinationDescriptor)
		if err != nil {
			return fmt.Errorf("parsing static route destination %q: %w", r.DestinationDescriptor, err)
		}

		gw, err := netip.ParseAddr(r.Router)
		if err != nil {
			return fmt.Errorf("parsing static route router %q: %w", r.Router, err)
		}

		cfg.Routes = append(cfg.Routes, Route{
			Family:      familyOf(dst.Addr()),
			Destination: dst.Masked().String(),
			Gateway:     gw.Unmap().String(),
			OutLinkName: attachTo,
			Table:       tableMain,
			Scope:       scopeGlobal,
			Type:        routeUnicast,
			Protocol:    protocolStatic,
			Layer:       layerPlatform,
		})
	}

	return nil
}

// vlanLink returns the link an address with VLAN ID vlanID goes on: parent
// itself when there is no VLAN, otherwise the VLAN link <parent>.<id>, which
// is added to cfg the first time it is needed.
func vlanLink(cfg *Config, parent, vlanID string) (string, error) {
	vlanID = strings.TrimSpace(vlanID)
	if vlanID == "" {
		return parent, nil
	}

	if strings.Contains(vlanID, ",") {
		return "", fmt.Errorf("vlanID %q lists several VLANs; only one can carry the address", vlanID)
	}

	vid, err := strconv.ParseUint(vlanID, 10, 16)
	if err != nil {
		return "", fmt.Errorf("parsing vlanID %q: %w", vlanID, err)
	}

	// VLAN 0 only carries priority tags: the address stays untagged.
	if vid == 0 {
		return parent, nil
	}

	if vid > 4094 {
		return "", fmt.Errorf("vlanID %d is out of range 1-4094", vid)
	}

	name := fmt.Sprintf("%s.%d", parent, vid)
	if len(name) > maxLinkName {
		return "", fmt.Errorf("VLAN link name %q is longer than %d characters", name, maxLinkName)
	}

	if !slices.ContainsFunc(cfg.Links, func(l Link) bool { return l.Name == name }) {
		cfg.Links = append(cfg.Links, Link{
			Name:       name,
			Logical:    true,
			Up:         true,
			Kind:       linkKindVLAN,
			Type:       linkTypeEther,
			ParentName: parent,
			VLAN:       &VLAN{ID: uint16(vid), Protocol: vlanProtocol},
			Layer:      layerPlatform,
		})
	}

	return name, nil
}

// addMachineSettings collects the hostname, resolvers and time servers from
// the addresses of the configured interfaces, in link order. Like smee, the
// first search domain is the domain name and name servers given by host name
// are skipped, since Talos resolvers are addresses.
func addMachineSettings(cfg *Config, nics []nic) {
	var hostname, domain string

	var nameServers, searchDomains, timeServers []string

	for _, n := range nics {
		if n.ipam == nil {
			continue
		}

		for _, ip := range []*hardware.IP{n.ipam.IPv4, n.ipam.IPv6} {
			if ip == nil || ip.Address == "" {
				continue
			}

			if hostname == "" {
				hostname = strings.TrimSpace(ip.Hostname)
			}

			if domain == "" && len(ip.DomainSearchList) > 0 {
				domain = strings.TrimSpace(ip.DomainSearchList[0])
			}

			for _, ns := range ip.Nameservers {
				if addr, err := netip.ParseAddr(strings.TrimSpace(ns)); err == nil {
					nameServers = appendUnique(nameServers, addr.Unmap().String())
				}
			}

			searchDomains = appendUnique(searchDomains, ip.DomainSearchList...)
			timeServers = appendUnique(timeServers, ip.NTPServers...)
		}
	}

	if hostname != "" {
		host, dom := splitFQDN(hostname)
		if dom == "" {
			dom = domain
		}

		cfg.Hostnames = append(cfg.Hostnames, Hostname{Hostname: host, Domainname: dom, Layer: layerPlatform})
	}

	// A platform resolver without servers would replace Talos' default servers
	// of the family it lacks, so search domains only travel with name servers.
	if len(nameServers) > 0 {
		cfg.Resolvers = append(cfg.Resolvers, Resolver{DNSServers: nameServers, SearchDomains: searchDomains, Layer: layerPlatform})
	}

	if len(timeServers) > 0 {
		cfg.TimeServers = append(cfg.TimeServers, TimeServer{Servers: timeServers, Layer: layerPlatform})
	}
}

func hasAddress(ipam *hardware.IPAM) bool {
	return ipam != nil && ((ipam.IPv4 != nil && ipam.IPv4.Address != "") || (ipam.IPv6 != nil && ipam.IPv6.Address != ""))
}

func sameAddress(a, b string) bool {
	pa, errA := netip.ParseAddr(strings.TrimSpace(a))
	pb, errB := netip.ParseAddr(strings.TrimSpace(b))

	if errA != nil || errB != nil {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}

	return pa.Unmap() == pb.Unmap()
}

// compareLinkNames orders link names naturally, so eth2 sorts before eth10.
func compareLinkNames(a, b string) int {
	ap, an, aok := splitTrailingNumber(a)
	bp, bn, bok := splitTrailingNumber(b)

	if aok && bok && ap == bp {
		return cmp.Compare(an, bn)
	}

	return strings.Compare(a, b)
}

func splitTrailingNumber(name string) (string, int, bool) {
	i := len(name)
	for i > 0 && name[i-1] >= '0' && name[i-1] <= '9' {
		i--
	}

	if i == len(name) {
		return name, 0, false
	}

	n, err := strconv.Atoi(name[i:])
	if err != nil {
		return name, 0, false
	}

	return name[:i], n, true
}

// PrefixLength converts a netmask to a prefix length. The netmask may be a
// dotted quad (255.255.255.0), an IPv6 mask, or a bare prefix length with or
// without a leading slash. family is 4 or 6.
func PrefixLength(netmask string, family int64) (int, error) {
	netmask = strings.TrimPrefix(strings.TrimSpace(netmask), "/")
	if netmask == "" {
		return 0, errors.New("netmask is empty")
	}

	maxBits := 32
	if family == 6 {
		maxBits = 128
	}

	if n, err := strconv.Atoi(netmask); err == nil {
		if n < 0 || n > maxBits {
			return 0, fmt.Errorf("prefix length %d is out of range for IPv%d", n, family)
		}

		return n, nil
	}

	addr, err := netip.ParseAddr(netmask)
	if err != nil {
		return 0, fmt.Errorf("netmask %q is neither a prefix length nor an address", netmask)
	}

	if addr.BitLen() != maxBits {
		return 0, fmt.Errorf("netmask %q does not match IPv%d", netmask, family)
	}

	ones, bits := net.IPMask(addr.AsSlice()).Size()
	if bits == 0 {
		return 0, fmt.Errorf("netmask %q is not contiguous", netmask)
	}

	return ones, nil
}

func familyOf(addr netip.Addr) string {
	if addr.Is4() {
		return familyInet4
	}

	return familyInet6
}

func familyNumber(addr netip.Addr) int64 {
	if addr.Is4() {
		return 4
	}

	return 6
}

func defaultRoutePriority(addr netip.Addr) uint32 {
	if addr.Is4() {
		return priorityInet4
	}

	return priorityInet6
}

// splitFQDN separates a host name from its domain at the first dot.
func splitFQDN(name string) (host, domain string) {
	host, domain, _ = strings.Cut(name, ".")

	return host, domain
}

func appendUnique(list []string, values ...string) []string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}

		duplicate := false

		for _, existing := range list {
			if existing == v {
				duplicate = true

				break
			}
		}

		if !duplicate {
			list = append(list, v)
		}
	}

	return list
}
