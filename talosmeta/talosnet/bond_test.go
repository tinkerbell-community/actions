package talosnet

import (
	"strings"
	"testing"
)

func TestBondValidateDefaults(t *testing.T) {
	b := &Bond{Mode: " Active-Backup "}
	if err := b.Validate(); err != nil {
		t.Fatalf("Validate() error: %v", err)
	}

	if b.Name != DefaultBondName || b.Mode != "active-backup" || b.XmitHashPolicy != "layer3+4" || b.LACPRate != "" {
		t.Fatalf("unexpected defaults: %+v", b)
	}
	if b.MIIMon == nil || *b.MIIMon != DefaultBondMIIMon {
		t.Fatalf("expected miimon %d, got %v", DefaultBondMIIMon, b.MIIMon)
	}
}

func TestBondValidateErrors(t *testing.T) {
	tests := []struct {
		name string
		bond Bond
		want string
	}{
		{"no mode", Bond{}, "bond mode is empty"},
		{"unknown mode", Bond{Mode: "lacp"}, "bond mode"},
		{"unknown hash policy", Bond{Mode: "802.3ad", XmitHashPolicy: "layer4"}, "transmit hash policy"},
		{"LACP rate outside 802.3ad", Bond{Mode: "active-backup", LACPRate: "fast"}, "only applies to bond mode 802.3ad"},
		{"unknown LACP rate", Bond{Mode: "802.3ad", LACPRate: "medium"}, "LACP rate"},
		{"name too long", Bond{Name: "bond-with-a-long-name", Mode: "802.3ad"}, "not a valid link name"},
		{"name with a slash", Bond{Name: "bond/0", Mode: "802.3ad"}, "not a valid link name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.bond.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected an error containing %q, got %v", tt.want, err)
			}
		})
	}
}

// The bond link carries the defaults Talos' FillDefaults would apply, since the
// metal platform does not apply them to META documents.
func TestBondLinkModeDependentDefaults(t *testing.T) {
	tests := []struct {
		mode           string
		tlbDynamicLB   uint8
		adActorSysPrio uint16
		adLACPActive   string
		missedMax      uint8
	}{
		{mode: "802.3ad", tlbDynamicLB: 1, adActorSysPrio: 65535, adLACPActive: "on"},
		{mode: "active-backup", tlbDynamicLB: 1, missedMax: 2},
		{mode: "balance-rr", tlbDynamicLB: 1, missedMax: 2},
		{mode: "balance-tlb"},
		{mode: "balance-alb"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			b := &Bond{Mode: tt.mode}
			if err := b.Validate(); err != nil {
				t.Fatalf("Validate() error: %v", err)
			}

			link := b.link()
			m := link.BondMaster
			if link.Name != "bond0" || !link.Logical || link.Kind != "bond" || link.Type != "ether" || m == nil {
				t.Fatalf("unexpected bond link: %+v", link)
			}
			if !m.UseCarrier || m.ResendIGMP != 1 || m.LPInterval != 1 || m.PacketsPerSlave != 1 || m.NumPeerNotif != 1 {
				t.Fatalf("missing kernel defaults: %+v", m)
			}
			if m.TLBDynamicLB != tt.tlbDynamicLB || m.ADActorSysPrio != tt.adActorSysPrio || m.ADLACPActive != tt.adLACPActive || m.MissedMax != tt.missedMax {
				t.Fatalf("unexpected mode-dependent settings: %+v", m)
			}
		})
	}
}

func TestBondLinkMIIMon(t *testing.T) {
	disabled := uint32(0)
	b := &Bond{Mode: "active-backup", MIIMon: &disabled}
	if err := b.Validate(); err != nil {
		t.Fatalf("Validate() error: %v", err)
	}
	if m := b.link().BondMaster; m.MIIMon != 0 || m.UpDelay != 0 || m.DownDelay != 0 {
		t.Fatalf("expected monitoring disabled, got %+v", m)
	}

	custom := uint32(250)
	b = &Bond{Mode: "active-backup", MIIMon: &custom}
	if err := b.Validate(); err != nil {
		t.Fatalf("Validate() error: %v", err)
	}
	if m := b.link().BondMaster; m.MIIMon != 250 || m.UpDelay != 500 || m.DownDelay != 500 {
		t.Fatalf("expected miimon 250 with 500 ms delays, got %+v", m)
	}
}
