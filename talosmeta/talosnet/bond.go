package talosnet

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

const (
	// DefaultBondName is the bond created when no name is given.
	DefaultBondName = "bond0"
	// DefaultBondMIIMon is the link monitoring interval, in milliseconds, used
	// when none is given. Talos' Equinix Metal platform bonds use the same.
	DefaultBondMIIMon = 100
	// defaultBondXmitHashPolicy is what Talos' Equinix Metal platform bonds
	// use; the kernel default, layer2, sends all traffic to the gateway down
	// one link.
	defaultBondXmitHashPolicy = "layer3+4"

	bondMode8023AD = "802.3ad"
	bondModeTLB    = "balance-tlb"
	bondModeALB    = "balance-alb"

	// maxLinkName is IFNAMSIZ less the terminating NUL.
	maxLinkName = 15
)

// The names Talos' nethelpers enums accept, see BondMode, BondXmitHashPolicy
// and LACPRate in github.com/siderolabs/talos/pkg/machinery/nethelpers.
var (
	bondModes = []string{
		"balance-rr", "active-backup", "balance-xor", "broadcast",
		bondMode8023AD, bondModeTLB, bondModeALB,
	}
	bondXmitHashPolicies = []string{"layer2", "layer3+4", "layer2+3", "encap2+3", "encap3+4"}
	lacpRates            = []string{"slow", "fast"}
)

// Bond enslaves every interface of the Hardware to one bond link, which then
// carries the addresses and routes.
type Bond struct {
	// Name of the bond link; DefaultBondName when empty.
	Name string
	// Mode is the bonding mode, for example 802.3ad or active-backup.
	Mode string
	// XmitHashPolicy selects the slave for outgoing traffic; layer3+4 when
	// empty.
	XmitHashPolicy string
	// LACPRate is slow or fast; only valid in 802.3ad mode. Empty keeps the
	// kernel default, slow.
	LACPRate string
	// MIIMon is the link monitoring interval in milliseconds; nil means
	// DefaultBondMIIMon, 0 disables monitoring.
	MIIMon *uint32
}

// Validate normalises the settings and fills in the defaults.
func (b *Bond) Validate() error {
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" {
		b.Name = DefaultBondName
	}

	if len(b.Name) > maxLinkName || strings.ContainsAny(b.Name, "/: \t\n") {
		return fmt.Errorf("bond name %q is not a valid link name", b.Name)
	}

	var err error

	if b.Mode, err = oneOf("bond mode", b.Mode, bondModes); err != nil {
		return err
	}

	if strings.TrimSpace(b.XmitHashPolicy) == "" {
		b.XmitHashPolicy = defaultBondXmitHashPolicy
	}

	if b.XmitHashPolicy, err = oneOf("bond transmit hash policy", b.XmitHashPolicy, bondXmitHashPolicies); err != nil {
		return err
	}

	if strings.TrimSpace(b.LACPRate) != "" {
		if b.Mode != bondMode8023AD {
			return fmt.Errorf("the LACP rate only applies to bond mode %s, not %s", bondMode8023AD, b.Mode)
		}

		if b.LACPRate, err = oneOf("LACP rate", b.LACPRate, lacpRates); err != nil {
			return err
		}
	}

	if b.MIIMon == nil {
		b.MIIMon = new(uint32(DefaultBondMIIMon))
	}

	return nil
}

// link returns the bond link. The metal platform hands the META document to
// the network controllers as it is, without the defaulting Talos applies to
// the bonds it builds itself (networkadapter BondMasterSpec.FillDefaults), so
// those defaults are written out here. Without them Talos would send zeroes
// the kernel rejects, such as lp_interval 0.
func (b *Bond) link() Link {
	miimon := *b.MIIMon

	master := &BondMaster{
		Mode:            b.Mode,
		XmitHashPolicy:  b.XmitHashPolicy,
		LACPRate:        b.LACPRate,
		MIIMon:          miimon,
		UpDelay:         2 * miimon,
		DownDelay:       2 * miimon,
		UseCarrier:      true,
		ResendIGMP:      1,
		LPInterval:      1,
		PacketsPerSlave: 1,
		NumPeerNotif:    1,
	}

	if b.Mode != bondModeALB && b.Mode != bondModeTLB {
		master.TLBDynamicLB = 1
	}

	if b.Mode == bondMode8023AD {
		master.ADActorSysPrio = 65535
		master.ADLACPActive = "on"
	} else if b.Mode != bondModeALB && b.Mode != bondModeTLB {
		master.MissedMax = 2
	}

	return Link{
		Name:       b.Name,
		Logical:    true,
		Up:         true,
		Kind:       linkKindBond,
		Type:       linkTypeEther,
		BondMaster: master,
		Layer:      layerPlatform,
	}
}

func oneOf(what, value string, allowed []string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", errors.New(what + " is empty")
	}

	if !slices.Contains(allowed, value) {
		return "", fmt.Errorf("%s %q is not one of %s", what, value, strings.Join(allowed, ", "))
	}

	return value, nil
}
