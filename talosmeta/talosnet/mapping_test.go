package talosnet

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/tinkerbell/actions/talosmeta/hardware"
)

type staticNamer map[string]string

func (s staticNamer) Name(mac string) (string, error) {
	if name, ok := s[mac]; ok {
		return name, nil
	}
	return "", errors.New("unknown mac " + mac)
}

const (
	mac1 = "52:54:00:12:34:01"
	mac2 = "52:54:00:12:34:02"
)

func ipv4(address, prefix, gateway string) *hardware.IP {
	return &hardware.IP{Address: address, Prefix: prefix, Gateway: gateway}
}

func withIPAM(ipam *hardware.IPAM) hardware.NetworkInterface {
	return hardware.NetworkInterface{IPAM: ipam}
}

func specOf(ifaces map[string]hardware.NetworkInterface) *hardware.Spec {
	return &hardware.Spec{NetworkInterfaces: ifaces}
}

func decodeYAML(t *testing.T, doc []byte) any {
	t.Helper()
	var out any
	if err := yaml.Unmarshal(doc, &out); err != nil {
		t.Fatalf("invalid yaml: %v\n%s", err, doc)
	}
	return out
}

func assertYAML(t *testing.T, cfg *Config, want string) {
	t.Helper()
	got, err := cfg.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if !reflect.DeepEqual(decodeYAML(t, got), decodeYAML(t, []byte(want))) {
		t.Fatalf("unexpected document\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func linkNames(cfg *Config) []string {
	names := make([]string, 0, len(cfg.Links))
	for _, l := range cfg.Links {
		names = append(names, l.Name)
	}
	return names
}

func TestFromHardwareStaticIPv4Interface(t *testing.T) {
	ip := ipv4("10.0.80.10", "24", "10.0.80.1")
	ip.Hostname = "node1"
	ip.DomainSearchList = []string{"example.com", "lab.example.com"}
	ip.Nameservers = []string{"10.0.0.2", "10.0.0.2", "dns.example.com"}
	ip.NTPServers = []string{"10.0.0.3", "time.example.com"}
	ip.ClasslessStaticRoutes = []hardware.ClasslessStaticRoute{{DestinationDescriptor: "192.168.0.0/16", Router: "10.0.80.254"}}

	spec := specOf(map[string]hardware.NetworkInterface{mac1: withIPAM(&hardware.IPAM{IPv4: ip})})

	cfg, err := FromHardware(spec, Options{Namer: staticNamer{mac1: "eth0"}})
	if err != nil {
		t.Fatalf("FromHardware() error: %v", err)
	}

	assertYAML(t, cfg, `
addresses:
    - address: 10.0.80.10/24
      linkName: eth0
      family: inet4
      scope: global
      flags: permanent
      layer: platform
links:
    - name: eth0
      up: true
      layer: platform
routes:
    - family: inet4
      gateway: 10.0.80.1
      outLinkName: eth0
      table: main
      priority: 1024
      scope: global
      type: unicast
      protocol: static
      layer: platform
    - family: inet4
      dst: 192.168.0.0/16
      gateway: 10.0.80.254
      outLinkName: eth0
      table: main
      scope: global
      type: unicast
      protocol: static
      layer: platform
hostnames:
    - hostname: node1
      domainname: example.com
      layer: platform
resolvers:
    - dnsServers:
        - 10.0.0.2
      searchDomains:
        - example.com
        - lab.example.com
      layer: platform
timeServers:
    - timeServers:
        - 10.0.0.3
        - time.example.com
      layer: platform
operators: []
externalIPs: []
`)
}

func TestFromHardwareDualStackInterface(t *testing.T) {
	spec := specOf(map[string]hardware.NetworkInterface{mac1: withIPAM(&hardware.IPAM{
		IPv4: ipv4("10.0.80.10", "24", "10.0.80.1"),
		IPv6: &hardware.IP{Address: "2001:db8::10", Prefix: "64", Gateway: "2001:db8::1"},
	})})

	cfg, err := FromHardware(spec, Options{Namer: staticNamer{mac1: "eth0"}})
	if err != nil {
		t.Fatalf("FromHardware() error: %v", err)
	}

	if len(cfg.Addresses) != 2 || cfg.Addresses[0].Address != "10.0.80.10/24" || cfg.Addresses[1].Address != "2001:db8::10/64" || cfg.Addresses[1].Family != "inet6" {
		t.Fatalf("unexpected addresses: %+v", cfg.Addresses)
	}
	if len(cfg.Routes) != 2 || cfg.Routes[0].Priority != 1024 || cfg.Routes[1].Priority != 2048 || cfg.Routes[1].Gateway != "2001:db8::1" {
		t.Fatalf("unexpected routes: %+v", cfg.Routes)
	}
	if !reflect.DeepEqual(linkNames(cfg), []string{"eth0"}) {
		t.Fatalf("expected only eth0, got %v", linkNames(cfg))
	}
}

func TestFromHardwareVLANAttachesAddressToVLANLink(t *testing.T) {
	v4 := ipv4("10.0.80.10", "24", "10.0.80.1")
	v4.VLANID = "100"
	v6 := &hardware.IP{Address: "2001:db8::10", Prefix: "64", VLANID: "100"}
	spec := specOf(map[string]hardware.NetworkInterface{mac1: withIPAM(&hardware.IPAM{IPv4: v4, IPv6: v6})})

	cfg, err := FromHardware(spec, Options{Namer: staticNamer{mac1: "eth0"}})
	if err != nil {
		t.Fatalf("FromHardware() error: %v", err)
	}

	if !reflect.DeepEqual(linkNames(cfg), []string{"eth0", "eth0.100"}) {
		t.Fatalf("expected eth0 and one eth0.100, got %v", linkNames(cfg))
	}
	vlan := cfg.Links[1]
	if !vlan.Logical || vlan.Kind != "vlan" || vlan.ParentName != "eth0" || vlan.VLAN == nil || vlan.VLAN.ID != 100 {
		t.Fatalf("unexpected VLAN link: %+v", vlan)
	}
	for _, a := range cfg.Addresses {
		if a.LinkName != "eth0.100" {
			t.Fatalf("expected addresses on eth0.100, got %+v", a)
		}
	}
	if cfg.Routes[0].OutLinkName != "eth0.100" {
		t.Fatalf("expected the default route out of eth0.100, got %+v", cfg.Routes[0])
	}
}

func TestFromHardwareVLANZeroIsUntagged(t *testing.T) {
	ip := ipv4("10.0.80.10", "24", "")
	ip.VLANID = "0"
	spec := specOf(map[string]hardware.NetworkInterface{mac1: withIPAM(&hardware.IPAM{IPv4: ip})})

	cfg, err := FromHardware(spec, Options{Namer: staticNamer{mac1: "eth0"}})
	if err != nil {
		t.Fatalf("FromHardware() error: %v", err)
	}
	if !reflect.DeepEqual(linkNames(cfg), []string{"eth0"}) || cfg.Addresses[0].LinkName != "eth0" {
		t.Fatalf("expected the address untagged on eth0, got links %v addresses %+v", linkNames(cfg), cfg.Addresses)
	}
}

func TestFromHardwareSkipsInterfacesWithoutAddress(t *testing.T) {
	spec := specOf(map[string]hardware.NetworkInterface{
		mac1: withIPAM(&hardware.IPAM{IPv4: ipv4("10.0.80.10", "24", "")}),
		// No address: left to DHCP, so it is never looked up.
		mac2: {},
	})

	cfg, err := FromHardware(spec, Options{Namer: staticNamer{mac1: "eth0"}})
	if err != nil {
		t.Fatalf("FromHardware() error: %v", err)
	}
	if !reflect.DeepEqual(linkNames(cfg), []string{"eth0"}) {
		t.Fatalf("expected only eth0, got %v", linkNames(cfg))
	}
}

func TestFromHardwareOrdersLinksByIndex(t *testing.T) {
	// The lower MAC has the higher kernel index.
	spec := specOf(map[string]hardware.NetworkInterface{
		mac1: withIPAM(&hardware.IPAM{IPv4: ipv4("10.0.1.10", "24", "")}),
		mac2: withIPAM(&hardware.IPAM{IPv4: ipv4("10.0.2.10", "24", "")}),
	})

	cfg, err := FromHardware(spec, Options{Namer: staticNamer{mac1: "eth10", mac2: "eth2"}})
	if err != nil {
		t.Fatalf("FromHardware() error: %v", err)
	}
	if !reflect.DeepEqual(linkNames(cfg), []string{"eth2", "eth10"}) {
		t.Fatalf("expected eth2 before eth10, got %v", linkNames(cfg))
	}
	if cfg.Addresses[0].LinkName != "eth2" || cfg.Addresses[0].Address != "10.0.2.10/24" {
		t.Fatalf("expected the eth2 address first, got %+v", cfg.Addresses)
	}
}

func TestFromHardwareBondsEveryInterface(t *testing.T) {
	ip := ipv4("10.0.80.10", "24", "10.0.80.1")
	ip.Nameservers = []string{"10.0.0.2"}
	spec := specOf(map[string]hardware.NetworkInterface{
		// The address is on the second NIC; the first has none.
		mac1: {},
		mac2: withIPAM(&hardware.IPAM{IPv4: ip}),
	})

	cfg, err := FromHardware(spec, Options{
		Namer: staticNamer{mac1: "eth0", mac2: "eth1"},
		Bond:  &Bond{Mode: "802.3AD", LACPRate: "fast"},
	})
	if err != nil {
		t.Fatalf("FromHardware() error: %v", err)
	}

	assertYAML(t, cfg, `
addresses:
    - address: 10.0.80.10/24
      linkName: bond0
      family: inet4
      scope: global
      flags: permanent
      layer: platform
links:
    - name: bond0
      logical: true
      up: true
      kind: bond
      type: ether
      bondMaster:
        mode: 802.3ad
        xmitHashPolicy: layer3+4
        lacpRate: fast
        miimon: 100
        updelay: 200
        downdelay: 200
        resendIgmp: 1
        lpInterval: 1
        packetsPerSlave: 1
        numPeerNotif: 1
        tlbLogicalLb: 1
        useCarrier: true
        adActorSysPrio: 65535
        adLacpActive: "on"
      layer: platform
    - name: eth0
      up: true
      masterName: bond0
      layer: platform
    - name: eth1
      up: true
      masterName: bond0
      slaveIndex: 1
      layer: platform
routes:
    - family: inet4
      gateway: 10.0.80.1
      outLinkName: bond0
      table: main
      priority: 1024
      scope: global
      type: unicast
      protocol: static
      layer: platform
hostnames: []
resolvers:
    - dnsServers:
        - 10.0.0.2
      layer: platform
timeServers: []
operators: []
externalIPs: []
`)
}

func TestFromHardwareBondWithVLAN(t *testing.T) {
	ip := ipv4("10.0.80.10", "24", "10.0.80.1")
	ip.VLANID = "80"
	spec := specOf(map[string]hardware.NetworkInterface{
		mac1: withIPAM(&hardware.IPAM{IPv4: ip}),
		mac2: {},
	})

	cfg, err := FromHardware(spec, Options{
		Namer: staticNamer{mac1: "eth0", mac2: "eth1"},
		Bond:  &Bond{Name: "bond1", Mode: "active-backup"},
	})
	if err != nil {
		t.Fatalf("FromHardware() error: %v", err)
	}

	if !reflect.DeepEqual(linkNames(cfg), []string{"bond1", "eth0", "eth1", "bond1.80"}) {
		t.Fatalf("unexpected links %v", linkNames(cfg))
	}
	if vlan := cfg.Links[3]; vlan.ParentName != "bond1" || vlan.VLAN == nil || vlan.VLAN.ID != 80 {
		t.Fatalf("expected VLAN 80 on bond1, got %+v", vlan)
	}
	if cfg.Addresses[0].LinkName != "bond1.80" || cfg.Routes[0].OutLinkName != "bond1.80" {
		t.Fatalf("expected the address and route on bond1.80, got %+v %+v", cfg.Addresses, cfg.Routes)
	}
}

func TestFromHardwareBondAcceptsTheSameAddressOnSeveralInterfaces(t *testing.T) {
	spec := specOf(map[string]hardware.NetworkInterface{
		mac1: withIPAM(&hardware.IPAM{IPv4: ipv4("10.0.80.10", "24", "10.0.80.1")}),
		mac2: withIPAM(&hardware.IPAM{IPv4: ipv4("10.0.80.10", "24", "10.0.80.1")}),
	})

	cfg, err := FromHardware(spec, Options{Namer: staticNamer{mac1: "eth0", mac2: "eth1"}, Bond: &Bond{Mode: "balance-alb"}})
	if err != nil {
		t.Fatalf("FromHardware() error: %v", err)
	}
	if len(cfg.Addresses) != 1 || len(cfg.Routes) != 1 {
		t.Fatalf("expected one address and one route, got %+v %+v", cfg.Addresses, cfg.Routes)
	}
}

func TestFromHardwareErrors(t *testing.T) {
	good := func() *hardware.IP { return ipv4("10.0.80.10", "24", "10.0.80.1") }
	namer := staticNamer{mac1: "eth0", mac2: "eth1"}

	tests := []struct {
		name string
		spec *hardware.Spec
		opts Options
		want string
	}{
		{
			name: "no interfaces",
			spec: specOf(nil),
			opts: Options{Namer: namer},
			want: "no networkInterfaces",
		},
		{
			name: "no address anywhere",
			spec: specOf(map[string]hardware.NetworkInterface{mac1: {}}),
			opts: Options{Namer: namer, Bond: &Bond{Mode: "active-backup"}},
			want: "no interface has a static address",
		},
		{
			name: "unknown MAC",
			spec: specOf(map[string]hardware.NetworkInterface{"52:54:00:ff:ff:ff": withIPAM(&hardware.IPAM{IPv4: good()})}),
			opts: Options{Namer: namer},
			want: "resolving link name",
		},
		{
			name: "bond needs every interface on the machine",
			spec: specOf(map[string]hardware.NetworkInterface{mac1: withIPAM(&hardware.IPAM{IPv4: good()}), "52:54:00:ff:ff:ff": {}}),
			opts: Options{Namer: namer, Bond: &Bond{Mode: "active-backup"}},
			want: "resolving link name",
		},
		{
			name: "two MACs on one link",
			spec: specOf(map[string]hardware.NetworkInterface{mac1: withIPAM(&hardware.IPAM{IPv4: good()}), mac2: withIPAM(&hardware.IPAM{IPv4: ipv4("10.0.81.10", "24", "")})}),
			opts: Options{Namer: staticNamer{mac1: "eth0", mac2: "eth0"}},
			want: "both resolve to link eth0",
		},
		{
			name: "bonded interfaces disagree",
			spec: specOf(map[string]hardware.NetworkInterface{mac1: withIPAM(&hardware.IPAM{IPv4: good()}), mac2: withIPAM(&hardware.IPAM{IPv4: ipv4("10.0.80.11", "24", "")})}),
			opts: Options{Namer: namer, Bond: &Bond{Mode: "802.3ad"}},
			want: "different IPv4 addresses",
		},
		{
			name: "invalid bond",
			spec: specOf(map[string]hardware.NetworkInterface{mac1: withIPAM(&hardware.IPAM{IPv4: good()})}),
			opts: Options{Namer: namer, Bond: &Bond{Mode: "lacp"}},
			want: "bond mode",
		},
		{
			name: "invalid address",
			spec: specOf(map[string]hardware.NetworkInterface{mac1: withIPAM(&hardware.IPAM{IPv4: ipv4("10.0.80", "24", "")})}),
			opts: Options{Namer: namer},
			want: "parsing address",
		},
		{
			name: "IPv6 address in the ipv4 block",
			spec: specOf(map[string]hardware.NetworkInterface{mac1: withIPAM(&hardware.IPAM{IPv4: ipv4("2001:db8::10", "64", "")})}),
			opts: Options{Namer: namer},
			want: "not an IPv4 address",
		},
		{
			name: "missing prefix",
			spec: specOf(map[string]hardware.NetworkInterface{mac1: withIPAM(&hardware.IPAM{IPv4: ipv4("10.0.80.10", "", "")})}),
			opts: Options{Namer: namer},
			want: "prefix",
		},
		{
			name: "invalid gateway",
			spec: specOf(map[string]hardware.NetworkInterface{mac1: withIPAM(&hardware.IPAM{IPv4: ipv4("10.0.80.10", "24", "gw")})}),
			opts: Options{Namer: namer},
			want: "parsing gateway",
		},
		{
			name: "several VLANs",
			spec: specOf(map[string]hardware.NetworkInterface{mac1: withIPAM(&hardware.IPAM{IPv4: &hardware.IP{Address: "10.0.80.10", Prefix: "24", VLANID: "100,200"}})}),
			opts: Options{Namer: namer},
			want: "several VLANs",
		},
		{
			name: "VLAN out of range",
			spec: specOf(map[string]hardware.NetworkInterface{mac1: withIPAM(&hardware.IPAM{IPv4: &hardware.IP{Address: "10.0.80.10", Prefix: "24", VLANID: "4095"}})}),
			opts: Options{Namer: namer},
			want: "out of range",
		},
		{
			name: "invalid static route",
			spec: specOf(map[string]hardware.NetworkInterface{mac1: withIPAM(&hardware.IPAM{IPv4: &hardware.IP{Address: "10.0.80.10", Prefix: "24", ClasslessStaticRoutes: []hardware.ClasslessStaticRoute{{DestinationDescriptor: "10.1.0.0", Router: "10.0.80.1"}}}})}),
			opts: Options{Namer: namer},
			want: "static route destination",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FromHardware(tt.spec, tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected an error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestFromHardwareHostnameComesFromTheFirstInterface(t *testing.T) {
	first := ipv4("10.0.1.10", "24", "")
	first.Hostname = "node1"
	second := ipv4("10.0.2.10", "24", "")
	second.Hostname = "other"
	second.DomainSearchList = []string{"example.com"}
	spec := specOf(map[string]hardware.NetworkInterface{
		mac1: withIPAM(&hardware.IPAM{IPv4: second}),
		mac2: withIPAM(&hardware.IPAM{IPv4: first}),
	})

	cfg, err := FromHardware(spec, Options{Namer: staticNamer{mac1: "eth1", mac2: "eth0"}})
	if err != nil {
		t.Fatalf("FromHardware() error: %v", err)
	}
	if len(cfg.Hostnames) != 1 || cfg.Hostnames[0].Hostname != "node1" || cfg.Hostnames[0].Domainname != "example.com" {
		t.Fatalf("expected node1 / example.com, got %+v", cfg.Hostnames)
	}
}

func TestCompareLinkNames(t *testing.T) {
	names := []string{"eth10", "bond0", "eth2", "eth1", "enp1s0f1", "enp1s0f0"}
	want := []string{"bond0", "enp1s0f0", "enp1s0f1", "eth1", "eth2", "eth10"}

	sorted := append([]string(nil), names...)
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if compareLinkNames(sorted[j], sorted[i]) < 0 {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	if !reflect.DeepEqual(sorted, want) {
		t.Fatalf("got %v, want %v", sorted, want)
	}
}

func TestPrefixLength(t *testing.T) {
	tests := []struct {
		netmask string
		family  int64
		want    int
		wantErr bool
	}{
		{"255.255.255.0", 4, 24, false},
		{"255.255.255.255", 4, 32, false},
		{"0.0.0.0", 4, 0, false},
		{"24", 4, 24, false},
		{"/24", 4, 24, false},
		{"64", 6, 64, false},
		{"ffff:ffff:ffff:ffff::", 6, 64, false},
		{"255.255.0.255", 4, 0, true},
		{"33", 4, 0, true},
		{"129", 6, 0, true},
		{"", 4, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.netmask, func(t *testing.T) {
			got, err := PrefixLength(tt.netmask, tt.family)
			if (err != nil) != tt.wantErr {
				t.Fatalf("PrefixLength(%q) error = %v, wantErr %v", tt.netmask, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("PrefixLength(%q) = %d, want %d", tt.netmask, got, tt.want)
			}
		})
	}
}

func TestParseFailsOnInvalidYAML(t *testing.T) {
	if _, err := Parse([]byte("addresses: [")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestParseRoundTrip(t *testing.T) {
	doc := []byte("hostnames:\n    - hostname: node1\n      layer: platform\n")
	cfg, err := Parse(doc)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if len(cfg.Hostnames) != 1 || cfg.Hostnames[0].Hostname != "node1" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}
