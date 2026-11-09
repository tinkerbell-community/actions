package hardware

import (
	"strings"
	"testing"
)

func TestParseDecodesNetworkInterfaces(t *testing.T) {
	raw := `{
  "instance": {"userdata": "secret machine config"},
  "networkInterfaces": {
    "52:54:00:AB:CD:01": {
      "dhcp": {"ipv4": {"leaseTimeSeconds": 86400}},
      "ipam": {
        "ipv4": {
          "address": "10.0.80.10", "prefix": "24", "gateway": "10.0.80.1", "hostname": "node1",
          "vlanID": "80", "nameservers": ["10.0.0.2"], "ntpServers": ["10.0.0.3"],
          "domainSearchList": ["example.com"],
          "classlessStaticRoutes": [{"destinationDescriptor": "192.168.0.0/16", "router": "10.0.80.254"}]
        },
        "ipv6": {"address": "2001:db8::10", "prefix": "64"}
      },
      "netboot": {"disabled": true}
    },
    "52:54:00:ab:cd:02": {}
  }
}`

	spec, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	if len(spec.NetworkInterfaces) != 2 {
		t.Fatalf("expected 2 interfaces, got %d", len(spec.NetworkInterfaces))
	}

	iface, ok := spec.NetworkInterfaces["52:54:00:ab:cd:01"]
	if !ok {
		t.Fatalf("expected the MAC to be lower cased, got %v", spec.NetworkInterfaces)
	}

	v4 := iface.IPAM.IPv4
	if v4.Address != "10.0.80.10" || v4.Prefix != "24" || v4.Gateway != "10.0.80.1" || v4.Hostname != "node1" || v4.VLANID != "80" {
		t.Fatalf("unexpected ipv4: %+v", v4)
	}
	if len(v4.Nameservers) != 1 || len(v4.NTPServers) != 1 || len(v4.DomainSearchList) != 1 || len(v4.ClasslessStaticRoutes) != 1 {
		t.Fatalf("unexpected ipv4 lists: %+v", v4)
	}
	if v4.ClasslessStaticRoutes[0].Router != "10.0.80.254" {
		t.Fatalf("unexpected route: %+v", v4.ClasslessStaticRoutes[0])
	}
	if iface.IPAM.IPv6 == nil || iface.IPAM.IPv6.Address != "2001:db8::10" {
		t.Fatalf("unexpected ipv6: %+v", iface.IPAM.IPv6)
	}
	if spec.NetworkInterfaces["52:54:00:ab:cd:02"].IPAM != nil {
		t.Fatal("expected no IPAM on the second interface")
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"invalid json", `{not json`, "decoding Hardware spec"},
		{"v1alpha1 spec", `{"interfaces":[{"dhcp":{"mac":"52:54:00:12:34:01"}}]}`, "v1alpha1"},
		{"no interfaces", `{"instance":{"vars":{"a":"b"}}}`, "no networkInterfaces"},
		{"duplicate after lower casing", `{"networkInterfaces":{"52:54:00:ab:cd:01":{},"52:54:00:AB:CD:01":{}}}`, "more than once"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.raw))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected an error containing %q, got %v", tt.want, err)
			}
		})
	}
}
