```
quay.io/tinkerbell/actions/talosmeta:latest
```

This action writes [Talos Linux](https://www.talos.dev) metal platform network
configuration into the `META` partition of a disk that already holds a Talos
image. It maps the network interfaces of the machine's Tinkerbell v1alpha2
Hardware onto the Talos network configuration document and stores the YAML
under META key `0xa` (`MetalNetworkPlatformConfig`). This is what
`talosctl meta write 0xa "$(cat network.yaml)"` does on a running node, done
offline before the first boot.

Talos applies the document before it fetches its machine configuration, so a
node gets a static address, default route, hostname, DNS and NTP even when the
Tinkerbell DHCP server no longer serves it after provisioning. Several
interfaces can be bonded.

```yaml
apiVersion: tinkerbell.org/v1alpha2
kind: Task
metadata:
  name: talos-install
spec:
  actions:
  - name: stream-talos-image
    image: quay.io/tinkerbell/actions/oci2disk:latest
    timeoutSeconds: 600
    env:
      DEST_DISK: /dev/sda
      IMG_URL: 192.168.1.2:5000/talos/metal-amd64:v1.14.0
  - name: write-talos-network-config
    image: quay.io/tinkerbell/actions/talosmeta:latest
    timeoutSeconds: 60
    env:
      DEST_DISK: /dev/sda
      HARDWARE_SPEC: '{{ dict "networkInterfaces" (dig "networkInterfaces" (dict) .hardware.spec) | toJson }}'
      BOND_MODE: '{{ dig "instance" "vars" "bondMode" "" .hardware.spec }}'
```

## Environment Variables

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `DEST_DISK` | yes | | Block device holding the Talos image, for example `/dev/sda`. The partition named `META` is located through the GPT, so partition device nodes are not needed. |
| `HARDWARE_SPEC` | unless `NETWORK_CONFIG` is set | | The v1alpha2 Hardware `spec`, or any JSON object holding its `networkInterfaces`. See below. |
| `NETWORK_CONFIG` | no | | A complete Talos network configuration document. When set nothing else is consulted and the YAML is written verbatim, exactly like `talosctl meta write 0xa`. |
| `LINK_NAMING` | no | `index` | How interface names are derived: `index` or `predictable`; see below. |
| `BOND_MODE` | no | | Bond every interface of the Hardware into one bond: `balance-rr`, `active-backup`, `balance-xor`, `broadcast`, `802.3ad`, `balance-tlb` or `balance-alb`. Empty means no bond. |
| `BOND_NAME` | no | `bond0` | Name of the bond link. |
| `BOND_XMIT_HASH_POLICY` | no | `layer3+4` | `layer2`, `layer3+4`, `layer2+3`, `encap2+3` or `encap3+4`. |
| `BOND_LACP_RATE` | no | kernel default, `slow` | `slow` or `fast`; only valid with `BOND_MODE=802.3ad`. |
| `BOND_MIIMON` | no | `100` | Link monitoring interval in milliseconds; `0` disables it. The up and down delays are twice the interval. |

The `BOND_*` settings other than `BOND_MODE` are rejected when `BOND_MODE` is
empty. Re-running the action replaces key `0xa` and leaves every other META key
intact.

## Passing Hardware data from the Task

Tinkerbell renders every string of a v1alpha2 Task as a template with the
Hardware object under `.hardware`, so the Task hands the interfaces straight
to the action:

```yaml
HARDWARE_SPEC: '{{ dict "networkInterfaces" (dig "networkInterfaces" (dict) .hardware.spec) | toJson }}'
```

Only `networkInterfaces` is passed, so the machine configuration in
`spec.instance.userdata` never leaves the Hardware object. `dig` keeps the
render working when the field is absent. A v1alpha1 spec, with its
`interfaces` list, is rejected with a message saying so.

The bond settings can come from the Hardware too, for example from
`spec.instance.vars`, so machines sharing a Task can bond differently:

```yaml
BOND_MODE: '{{ dig "instance" "vars" "bondMode" "" .hardware.spec }}'
```

## Mapping

The document follows the Talos
[metal network configuration](https://docs.siderolabs.com/talos/latest/platform-specific-installations/bare-metal-platforms/metal-network-configuration)
format. Every entry is written on the `platform` layer, so the machine
configuration can still override it.

For each address in `networkInterfaces[].ipam.ipv4` and `ipam.ipv6`:

- the interface's link `{name, up: true}`; with `vlanID` set, also a logical
  VLAN link `<name>.<vid>` that carries the address and routes. A list of
  VLAN IDs is rejected, since only one can carry the address; `0` means
  untagged,
- an address `<address>/<prefix>` with `family`, `scope: global` and
  `flags: permanent`,
- a default route through `gateway` (`table: main`, priority `1024` for IPv4
  and `2048` for IPv6),
- one route per `classlessStaticRoutes` entry.

For the machine, from the interfaces with an address, in link order:

- `hostnames` from the first `hostname`; the domain is the first
  `domainSearchList` entry, which is also the domain name smee serves,
- `resolvers` from the union of the `nameservers` given as addresses (Talos
  resolvers are addresses, so host names are skipped, as smee does), with the
  `domainSearchList` entries as search domains,
- `timeServers` from the union of the `ntpServers`.

Interfaces without an address are left alone so Talos keeps using DHCP on
them, and are not looked up. The action fails when no interface has an
address.

## Bonding

With `BOND_MODE` set, every interface in `networkInterfaces` becomes a slave
of the bond, in link order, so the first one (`eth0`) gives the bond its MAC.
The addresses, VLAN and routes go on the bond instead of an interface. Usually
only the interface the machine netboots from has `ipam`; when several do, they
must agree on the address of each family. Every interface must exist on the
machine.

```yaml
links:
    - name: bond0
      logical: true
      up: true
      kind: bond
      type: ether
      bondMaster:
        mode: 802.3ad
        xmitHashPolicy: layer3+4
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
```

The defaults follow the bonds Talos builds for its Equinix Metal platform.
The metal platform passes the META document to the network controllers
unchanged, without the defaulting Talos applies to bonds it builds itself, so
the action writes those values (`lpInterval`, `resendIgmp` and the rest) out;
zeroes there are rejected by the kernel.

## Interface names

The name written to META has to be the one Talos will see.

- `LINK_NAMING=index` (default) names each interface `eth<N>`, where `N` is
  the index the installation environment's kernel gave it: the interface
  carrying the MAC is looked up in sysfs and must be named `eth<N>`. Talos
  booted with `net.ifnames=0` (for example through the `taloscmdline` action)
  keeps kernel names, so the names match as long as Talos finds the network
  devices in the same order. An installation environment that renames
  interfaces makes the index unknown, and the action fails; boot it with
  `net.ifnames=0` too.
- `LINK_NAMING=predictable` derives the name udev's predictable scheme gives
  the interface, which Talos uses without `net.ifnames=0`: onboard index
  (`eno<n>`), then PCI hotplug slot (`ens<n>`), then PCI path
  (`enp<bus>s<slot>[f<function>]`). Non-PCI devices keep the kernel name and a
  warning is logged.

Only physical interfaces are matched, so a VLAN or bond sharing the MAC in
the installation environment is ignored.

## Verifying on the node

```sh
talosctl -n <node> get meta 0x0a -o yaml
talosctl -n <node> get links
talosctl -n <node> get addresses
```
