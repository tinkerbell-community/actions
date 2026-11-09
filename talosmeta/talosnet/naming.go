package talosnet

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// SysfsNamer finds the physical interface that carries a MAC address by
// reading the sysfs of the installation environment (HookOS).
//
// By default it returns eth<N>, where N is the index the kernel gave the
// interface when it named it eth<N>. Talos booted with net.ifnames=0 keeps
// those kernel names, so the link names match as long as Talos finds the
// network devices in the same order the installation environment did. An
// interface the installation environment renamed has no known index and is an
// error.
//
// With Predictable set it instead derives the name udev's predictable naming
// scheme assigns (the scheme Talos uses unless booted with net.ifnames=0):
// onboard index (eno1), then PCI hotplug slot (ens3), then PCI path
// (enp1s0f1). When the device is not on PCI the kernel name is returned
// unchanged.
type SysfsNamer struct {
	// Root is the sysfs mount point; defaults to /sys.
	Root string
	// Predictable selects udev-style names instead of eth<N>.
	Predictable bool
	// Logger receives a warning when a predictable name cannot be derived.
	Logger *slog.Logger
}

const (
	defaultSysfsRoot = "/sys"
	// onboardIndexMax matches systemd's ONBOARD_INDEX_MAX.
	onboardIndexMax = 16383
)

var pciAddress = regexp.MustCompile(`^([0-9a-f]{4}):([0-9a-f]{2}):([0-9a-f]{2})\.([0-7])$`)

// kernelIndexName matches the names the kernel gives ethernet devices.
var kernelIndexName = regexp.MustCompile(`^eth[0-9]+$`)

// Name implements Namer.
func (n *SysfsNamer) Name(mac string) (string, error) {
	root := n.Root
	if root == "" {
		root = defaultSysfsRoot
	}

	netDir := filepath.Join(root, "class", "net")

	entries, err := os.ReadDir(netDir)
	if err != nil {
		return "", fmt.Errorf("listing %s: %w", netDir, err)
	}

	want := strings.ToLower(strings.TrimSpace(mac))

	for _, entry := range entries {
		linkDir := filepath.Join(netDir, entry.Name())

		address, err := os.ReadFile(filepath.Join(linkDir, "address"))
		if err != nil {
			continue
		}

		if strings.ToLower(strings.TrimSpace(string(address))) != want {
			continue
		}

		// VLAN, bond and bridge links can share the MAC of a physical
		// interface; only physical ones have a device.
		if _, err := os.Stat(filepath.Join(linkDir, "device")); err != nil {
			continue
		}

		kernelName := entry.Name()
		if !n.Predictable {
			if !kernelIndexName.MatchString(kernelName) {
				return "", fmt.Errorf("the installation environment named the interface with MAC %s %q, not eth<N>, so its kernel index is unknown; boot it with net.ifnames=0 or use predictable link naming", mac, kernelName)
			}

			return kernelName, nil
		}

		if name, ok := predictableName(root, linkDir); ok {
			return name, nil
		}

		if n.Logger != nil {
			n.Logger.Warn("Interface is not a PCI device, keeping the kernel name; provide NETWORK_CONFIG if Talos names it differently", "interface", kernelName, "mac", mac)
		}

		return kernelName, nil
	}

	return "", fmt.Errorf("no physical interface with MAC %s found under %s", mac, netDir)
}

func predictableName(root, linkDir string) (string, bool) {
	devicePath, err := filepath.EvalSymlinks(filepath.Join(linkDir, "device"))
	if err != nil {
		return "", false
	}

	pciDev := nearestPCIDevice(devicePath)
	if pciDev == "" {
		return "", false
	}

	for _, attr := range []string{"acpi_index", "index"} {
		if idx, ok := readOnboardIndex(filepath.Join(pciDev, attr)); ok {
			return "eno" + strconv.Itoa(idx), true
		}
	}

	if slot, ok := hotplugSlot(root, pciDev); ok {
		return "ens" + strconv.Itoa(slot) + functionSuffix(pciDev), true
	}

	return pciPathName(pciDev), true
}

// nearestPCIDevice walks up from path until it reaches a directory named like
// a PCI address, which is how virtio and other bus children reach their PCI
// parent.
func nearestPCIDevice(path string) string {
	for {
		if pciAddress.MatchString(filepath.Base(path)) {
			return path
		}

		parent := filepath.Dir(path)
		if parent == path {
			return ""
		}

		path = parent
	}
}

func readOnboardIndex(path string) (int, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}

	idx, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || idx <= 0 || idx > onboardIndexMax {
		return 0, false
	}

	return idx, true
}

// hotplugSlot reports the PCI hotplug slot number of the device or of one of
// its PCI ancestors, matching /sys/bus/pci/slots/<n>/address entries.
func hotplugSlot(root, pciDev string) (int, bool) {
	slotsDir := filepath.Join(root, "bus", "pci", "slots")

	entries, err := os.ReadDir(slotsDir)
	if err != nil {
		return 0, false
	}

	slots := map[string]int{}

	for _, entry := range entries {
		slot, err := strconv.Atoi(entry.Name())
		if err != nil || slot < 0 {
			continue
		}

		raw, err := os.ReadFile(filepath.Join(slotsDir, entry.Name(), "address"))
		if err != nil {
			continue
		}

		slots[strings.TrimSpace(string(raw))] = slot
	}

	for dev := pciDev; pciAddress.MatchString(filepath.Base(dev)); dev = filepath.Dir(dev) {
		address := strings.TrimSuffix(filepath.Base(dev), filepath.Ext(filepath.Base(dev)))
		if slot, ok := slots[address]; ok {
			return slot, true
		}
	}

	return 0, false
}

func pciPathName(pciDev string) string {
	m := pciAddress.FindStringSubmatch(filepath.Base(pciDev))
	domain, _ := strconv.ParseUint(m[1], 16, 16)
	bus, _ := strconv.ParseUint(m[2], 16, 8)
	slot, _ := strconv.ParseUint(m[3], 16, 8)

	var b strings.Builder

	b.WriteString("en")

	if domain != 0 {
		fmt.Fprintf(&b, "P%d", domain)
	}

	fmt.Fprintf(&b, "p%ds%d", bus, slot)
	b.WriteString(functionSuffix(pciDev))

	return b.String()
}

// functionSuffix returns "f<n>" when the PCI function is non-zero or the
// device is multi-function, matching systemd's net_id.
func functionSuffix(pciDev string) string {
	m := pciAddress.FindStringSubmatch(filepath.Base(pciDev))
	function, _ := strconv.ParseUint(m[4], 16, 8)

	if function == 0 && !isMultifunction(pciDev) {
		return ""
	}

	return "f" + strconv.FormatUint(function, 10)
}

func isMultifunction(pciDev string) bool {
	config, err := os.ReadFile(filepath.Join(pciDev, "config"))
	if err != nil || len(config) < 15 {
		return false
	}

	// PCI header type register, bit 7 flags a multi-function device.
	return config[14]&0x80 != 0
}
