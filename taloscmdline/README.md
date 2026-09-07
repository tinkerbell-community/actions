```
quay.io/tinkerbell/actions/taloscmdline:latest
```

This action sets kernel command line arguments inside the
[Talos Linux](https://www.talos.dev) unified kernel image (UKI) on a disk
that already holds a Talos image. Talos boots through systemd-boot on UEFI,
and the command line lives in the `.cmdline` section of
`EFI/Linux/Talos-<version>.efi` on the `EFI` partition rather than in a text
file. The action rewrites that section the way Talos' own image assembler lays
it out, so the result is a regular Talos UKI with a different command line.

The typical use is pointing a node at Tinkerbell's metadata service for its
machine configuration:

```yaml
actions:
- name: "stream-talos-image"
  image: quay.io/tinkerbell/actions/oci2disk:latest
  timeout: 600
  environment:
    DEST_DISK: /dev/sda
    IMG_URL: "192.168.1.2:5000/talos/metal-amd64:v1.14.0"
- name: "set-talos-config-url"
  image: quay.io/tinkerbell/actions/taloscmdline:latest
  timeout: 120
  environment:
    DEST_DISK: /dev/sda
    KERNEL_ARGS: "talos.config=http://192.168.1.2:7080/2009-04-04/user-data"
```

Talos then fetches its machine configuration from that URL on every boot, and
the installer copies `talos.config` into the UKI it generates on upgrades.

## Environment Variables

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `DEST_DISK` | yes | | Whole disk holding the Talos image, for example `/dev/sda`. The `EFI` partition is located through the GPT and mounted as `vfat`. |
| `KERNEL_ARGS` | yes | | Whitespace separated arguments to set. A `key=value` argument replaces every existing argument with that key; a bare flag is appended once. Other arguments are kept in place. |
| `UKI_PATTERN` | no | `EFI/Linux/Talos-*.efi` | Glob, relative to the EFI partition root, of the UKIs to edit. Every match is updated. |
| `STRIP_SIGNATURE` | no | `false` | Allow editing an Authenticode-signed UKI. The signature is removed, so the file only boots with Secure Boot disabled. Without this the action refuses signed UKIs. |

The action is idempotent: a UKI whose command line already contains the
requested arguments is left untouched. Each UKI is rewritten to a temporary
file next to it and renamed over the original.

## What is rewritten

Talos assembles a UKI by appending data sections (`.osrel`, `.cmdline`,
`.uname`, `.splash`, `.linux`, `.initrd`, profiles) to the systemd stub with
sequential, aligned virtual addresses. A Talos UKI carries one `.cmdline`
for the default boot and another, after the `.profile` sections, for the
"Reset Talos installation" boot entry; the action updates every one of them.
It keeps everything before the first `.cmdline` byte-identical, writes each
new command line with an aligned raw size, moves the later data sections by
the size difference, and recomputes the image size fields. Anything after the last section, such as a certificate
table, is dropped. It refuses to touch a file whose layout does not match
these assumptions, for example when a code section follows `.cmdline`.

## Limitations

- Only the UEFI path is changed. Talos dual-boot images also carry
  `grub/grub.cfg` on the `BOOT` partition for BIOS boots; that file is not
  modified.
- Secure Boot images are signed; rewriting them invalidates the signature and
  any TPM PCR policy signature. Use Talos' image factory `extraKernelArgs` for
  Secure Boot deployments instead.
- Arguments that legitimately repeat, such as `console=`, are collapsed to
  the supplied value when they are part of `KERNEL_ARGS`.

## Verifying

```
talosctl -n <node> read /proc/cmdline
```
