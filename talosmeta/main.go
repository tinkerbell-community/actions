package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/tinkerbell/actions/talosmeta/hardware"
	"github.com/tinkerbell/actions/talosmeta/meta"
	"github.com/tinkerbell/actions/talosmeta/talosnet"
)

const (
	linkNamingIndex       = "index"
	linkNamingPredictable = "predictable"
)

// sysfsRoot is where link names are looked up; tests point it at a fake tree.
var sysfsRoot = "/sys"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	logger.Info("TALOSMETA - Write Talos network configuration to the META partition")

	if err := run(logger); err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	disk := os.Getenv("DEST_DISK")
	if disk == "" {
		return errors.New("no block device specified with environment variable [DEST_DISK]")
	}

	doc, err := networkDocument(logger)
	if err != nil {
		return err
	}

	partition, err := meta.Locate(disk)
	if err != nil {
		return err
	}

	logger.Info("Found META partition", "disk", disk, "offset", partition.Offset, "size", partition.Size)

	if err := meta.WriteTag(disk, meta.MetalNetworkPlatformConfig, doc); err != nil {
		return err
	}

	logger.Info("Wrote network configuration to META", "key", fmt.Sprintf("%#x", meta.MetalNetworkPlatformConfig), "bytes", len(doc))
	logger.Info("Network configuration written:\n" + string(doc))

	return nil
}

// networkDocument returns the YAML to store under META key 0xa: the
// operator-supplied NETWORK_CONFIG document, or one built from the v1alpha2
// Hardware spec in HARDWARE_SPEC.
func networkDocument(logger *slog.Logger) ([]byte, error) {
	if raw := os.Getenv("NETWORK_CONFIG"); raw != "" {
		if _, err := talosnet.Parse([]byte(raw)); err != nil {
			return nil, fmt.Errorf("NETWORK_CONFIG: %w", err)
		}

		logger.Info("Using network configuration from NETWORK_CONFIG")

		return []byte(raw), nil
	}

	raw := os.Getenv("HARDWARE_SPEC")
	if raw == "" {
		return nil, errors.New("no network data: set [HARDWARE_SPEC] to the Hardware spec, or provide [NETWORK_CONFIG]")
	}

	spec, err := hardware.Parse([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("HARDWARE_SPEC: %w", err)
	}

	logger.Info("Using hardware data from HARDWARE_SPEC", "interfaces", len(spec.NetworkInterfaces))

	namer, err := newNamer(logger)
	if err != nil {
		return nil, err
	}

	bond, err := bondFromEnv()
	if err != nil {
		return nil, err
	}

	if bond != nil {
		logger.Info("Bonding the interfaces", "bond", bond.Name, "mode", bond.Mode)
	}

	cfg, err := talosnet.FromHardware(spec, talosnet.Options{Namer: namer, Bond: bond})
	if err != nil {
		return nil, fmt.Errorf("mapping hardware data to Talos network configuration: %w", err)
	}

	return cfg.Marshal()
}

func newNamer(logger *slog.Logger) (*talosnet.SysfsNamer, error) {
	mode, ok := os.LookupEnv("LINK_NAMING")
	if !ok || mode == "" {
		mode = linkNamingIndex
	}

	switch mode {
	case linkNamingIndex:
		return &talosnet.SysfsNamer{Root: sysfsRoot, Predictable: false, Logger: logger}, nil
	case linkNamingPredictable:
		return &talosnet.SysfsNamer{Root: sysfsRoot, Predictable: true, Logger: logger}, nil
	default:
		return nil, fmt.Errorf("LINK_NAMING must be %q or %q, got %q", linkNamingIndex, linkNamingPredictable, mode)
	}
}

// bondFromEnv returns the bond BOND_MODE asks for, or nil when it is unset.
// The settings are validated, and defaulted, by talosnet.FromHardware.
func bondFromEnv() (*talosnet.Bond, error) {
	mode := strings.TrimSpace(os.Getenv("BOND_MODE"))
	if mode == "" {
		for _, name := range []string{"BOND_NAME", "BOND_XMIT_HASH_POLICY", "BOND_LACP_RATE", "BOND_MIIMON"} {
			if strings.TrimSpace(os.Getenv(name)) != "" {
				return nil, fmt.Errorf("[%s] is set but [BOND_MODE] is not; set BOND_MODE to bond the interfaces", name)
			}
		}

		return nil, nil //nolint:nilnil // no bond requested
	}

	bond := &talosnet.Bond{
		Name:           os.Getenv("BOND_NAME"),
		Mode:           mode,
		XmitHashPolicy: os.Getenv("BOND_XMIT_HASH_POLICY"),
		LACPRate:       os.Getenv("BOND_LACP_RATE"),
	}

	if raw := strings.TrimSpace(os.Getenv("BOND_MIIMON")); raw != "" {
		miimon, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("BOND_MIIMON must be a number of milliseconds, got %q", raw)
		}

		bond.MIIMon = new(uint32(miimon))
	}

	if err := bond.Validate(); err != nil {
		return nil, fmt.Errorf("BOND_*: %w", err)
	}

	return bond, nil
}
