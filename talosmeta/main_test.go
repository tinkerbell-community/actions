package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/partition/gpt"

	"github.com/tinkerbell/actions/talosmeta/meta"
)

func newTalosLikeDisk(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "disk.img")
	d, err := diskfs.Create(path, 8*1024*1024, diskfs.SectorSize512)
	if err != nil {
		t.Fatal(err)
	}
	table := &gpt.Table{
		Partitions: []*gpt.Partition{
			{Index: 1, Start: 2048, End: 4095, Type: gpt.EFISystemPartition, Name: "EFI"},
			{Index: 2, Start: 4096, End: 6143, Type: gpt.LinuxFilesystem, Name: "META"},
		},
		LogicalSectorSize:  512,
		PhysicalSectorSize: 512,
		ProtectiveMBR:      true,
	}
	if err := d.Partition(table); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	return path
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunWritesNetworkConfigPassthrough(t *testing.T) {
	disk := newTalosLikeDisk(t)
	doc := "hostnames:\n    - hostname: node1\n      layer: platform\n"

	t.Setenv("DEST_DISK", disk)
	t.Setenv("NETWORK_CONFIG", doc)

	if err := run(quietLogger()); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	got, ok, err := meta.ReadTag(disk, meta.MetalNetworkPlatformConfig)
	if err != nil || !ok {
		t.Fatalf("expected tag 0xa to be written, ok=%v err=%v", ok, err)
	}
	if string(got) != doc {
		t.Fatalf("unexpected document written:\n%s", got)
	}
}

func TestRunRejectsInvalidPassthrough(t *testing.T) {
	t.Setenv("DEST_DISK", newTalosLikeDisk(t))
	t.Setenv("NETWORK_CONFIG", "addresses: [")

	if err := run(quietLogger()); err == nil || !strings.Contains(err.Error(), "NETWORK_CONFIG") {
		t.Fatalf("expected a NETWORK_CONFIG error, got %v", err)
	}
}

// fakeSysfs points link naming at a sysfs tree holding one physical
// interface per name, carrying the MAC it maps to.
func fakeSysfs(t *testing.T, links map[string]string) {
	t.Helper()

	root := t.TempDir()
	for name, mac := range links {
		dir := filepath.Join(root, "class", "net", name)
		device := filepath.Join(root, "devices", "pci0000:00", name)
		for _, d := range []string{dir, device} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "address"), []byte(mac+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(device, filepath.Join(dir, "device")); err != nil {
			t.Fatal(err)
		}
	}

	previous := sysfsRoot
	sysfsRoot = root
	t.Cleanup(func() { sysfsRoot = previous })
}

// clearEnv unsets the variables a test does not set, so the environment of
// the test run cannot leak in.
func clearEnv(t *testing.T) {
	t.Helper()

	for _, name := range []string{"NETWORK_CONFIG", "HARDWARE_SPEC", "LINK_NAMING", "BOND_MODE", "BOND_NAME", "BOND_XMIT_HASH_POLICY", "BOND_LACP_RATE", "BOND_MIIMON"} {
		t.Setenv(name, "")
	}
}

const hardwareSpecJSON = `{"networkInterfaces":{
"52:54:00:12:34:01":{"ipam":{"ipv4":{"address":"10.0.80.10","prefix":"24","gateway":"10.0.80.1","hostname":"node1",
"domainSearchList":["example.com"],"nameservers":["10.0.0.2"]}}},
"52:54:00:12:34:02":{}}}`

func TestRunRequiresDestDisk(t *testing.T) {
	clearEnv(t)
	t.Setenv("DEST_DISK", "")

	if err := run(quietLogger()); err == nil || !strings.Contains(err.Error(), "DEST_DISK") {
		t.Fatalf("expected a DEST_DISK error, got %v", err)
	}
}

func TestRunRequiresHardwareSpec(t *testing.T) {
	clearEnv(t)
	t.Setenv("DEST_DISK", newTalosLikeDisk(t))

	if err := run(quietLogger()); err == nil || !strings.Contains(err.Error(), "HARDWARE_SPEC") {
		t.Fatalf("expected a HARDWARE_SPEC error, got %v", err)
	}
}

func TestRunBuildsDocumentFromHardwareSpec(t *testing.T) {
	clearEnv(t)
	fakeSysfs(t, map[string]string{"eth0": "52:54:00:12:34:01", "eth1": "52:54:00:12:34:02"})

	disk := newTalosLikeDisk(t)
	t.Setenv("DEST_DISK", disk)
	t.Setenv("HARDWARE_SPEC", hardwareSpecJSON)

	if err := run(quietLogger()); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	got, ok, err := meta.ReadTag(disk, meta.MetalNetworkPlatformConfig)
	if err != nil || !ok {
		t.Fatalf("expected tag 0xa to be written, ok=%v err=%v", ok, err)
	}
	for _, want := range []string{"address: 10.0.80.10/24", "linkName: eth0", "gateway: 10.0.80.1", "hostname: node1", "domainname: example.com", "- 10.0.0.2"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("expected document to contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(string(got), "eth1") {
		t.Fatalf("expected the interface without an address to be left alone:\n%s", got)
	}
}

func TestRunBondsTheInterfaces(t *testing.T) {
	clearEnv(t)
	fakeSysfs(t, map[string]string{"eth0": "52:54:00:12:34:01", "eth1": "52:54:00:12:34:02"})

	disk := newTalosLikeDisk(t)
	t.Setenv("DEST_DISK", disk)
	t.Setenv("HARDWARE_SPEC", hardwareSpecJSON)
	t.Setenv("BOND_MODE", "802.3ad")
	t.Setenv("BOND_LACP_RATE", "fast")
	t.Setenv("BOND_MIIMON", "200")

	if err := run(quietLogger()); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	got, _, err := meta.ReadTag(disk, meta.MetalNetworkPlatformConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kind: bond", "mode: 802.3ad", "lacpRate: fast", "miimon: 200", "updelay: 400", "masterName: bond0", "slaveIndex: 1", "linkName: bond0", "outLinkName: bond0"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("expected document to contain %q:\n%s", want, got)
		}
	}
}

func TestRunRejectsBadSettings(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"unknown link naming", map[string]string{"LINK_NAMING": "kernel"}, "LINK_NAMING"},
		{"bond setting without a mode", map[string]string{"BOND_LACP_RATE": "fast"}, "BOND_MODE"},
		{"unknown bond mode", map[string]string{"BOND_MODE": "lacp"}, "bond mode"},
		{"invalid miimon", map[string]string{"BOND_MODE": "802.3ad", "BOND_MIIMON": "100ms"}, "BOND_MIIMON"},
		{"v1alpha1 spec", map[string]string{"HARDWARE_SPEC": `{"interfaces":[{"dhcp":{"mac":"52:54:00:12:34:01"}}]}`}, "v1alpha1"},
		{"invalid spec", map[string]string{"HARDWARE_SPEC": "{not json"}, "HARDWARE_SPEC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			fakeSysfs(t, map[string]string{"eth0": "52:54:00:12:34:01", "eth1": "52:54:00:12:34:02"})
			t.Setenv("DEST_DISK", newTalosLikeDisk(t))
			t.Setenv("HARDWARE_SPEC", hardwareSpecJSON)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			if err := run(quietLogger()); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected an error containing %q, got %v", tt.want, err)
			}
		})
	}
}
