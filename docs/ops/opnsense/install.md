> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Install OPNsense with the OOB daemon

This guide creates a fresh OPNsense VM and brings it to a working state with SSH, the
QEMU Guest Agent (QGA), and the `mwan-opnsense` out-of-band (OOB) daemon. For the
daemon's design and how to operate it once it runs, read
[OPNsense OOB daemon](daemon.md).

Run every command from the operator workstation against the target Proxmox host. The
examples use placeholders: set `VMID`, `LAN_IP`, and `PVE` (the Proxmox host) once and
reuse them. Test it on the suburban testbed, never against production or an existing
router VM.

## Stay inside the safety boundary

A wrong VMID destroys the wrong machine, so confirm the target before any destructive
command.

- Use an unused VMID and a unique LAN address on the testbed subnet.
- Before each create, set, or delete, confirm the command names only your chosen VMID.
- If an installer prompt is ambiguous, stop instead of guessing.

Confirm the VMID is free first:

```bash
ssh "$PVE" "qm list; qm config $VMID 2>&1 || true"
```

Proceed only if the VMID has no existing config.

## Create the VM with a named serial port

Create the VM with a serial console, QGA enabled, one management interface, and a separate
named virtio-serial port for the daemon. The named port is load-bearing: it keeps the
daemon off the raw device that the guest agent uses, so the two never collide.

```bash
ssh "$PVE" "qm create $VMID \
    --name opnsense \
    --memory 4096 --cores 2 --ostype other \
    --scsihw virtio-scsi-pci \
    --serial0 socket --vga serial0 \
    --net0 virtio,bridge=vmbr1 \
    --agent enabled=1 \
    --args '-device virtio-serial-pci,id=mwanrpc -chardev socket,id=mwanchr,path=/var/run/qemu-server/$VMID.mwanrpc,server=on,wait=off -device virtserialport,chardev=mwanchr,name=io.goodkind.mwan-opnsense.0'"
```

Attach the serial installer image, create a 16G or larger target disk, and boot the
installer. An 8G disk fails the ZFS install with `gpart: autofill: No space left on
device`.

```bash
ssh "$PVE" "qm set $VMID --scsi0 local-zfs:0,import-from=/var/lib/vz/template/iso/OPNsense-serial-amd64.img"
ssh "$PVE" "qm set $VMID --scsi1 local-zfs:16,discard=on"
ssh "$PVE" "qm set $VMID --boot order=scsi0"
ssh "$PVE" "qm start $VMID"
```

## Install OPNsense over the serial console

Attach a TTY to the serial console. A non-TTY `qm terminal` fails with `tcgetattr`.

```bash
ssh -tt "$PVE" "qm terminal $VMID"
```

Wait for the `login:` prompt, then log in as `installer` with password `opnsense`. Walk
the installer with these choices:

1. Keymap: continue with the default.
2. Install: choose Install (ZFS), stripe.
3. Disk: select the QEMU target with Space, then press Enter directly. Do not press Tab
   first, because Tab moves focus to Back.
4. Finish: choose Complete Install, then Halt.

After the VM halts, boot the installed disk:

```bash
ssh "$PVE" "qm set $VMID --boot order=scsi1"
ssh "$PVE" "qm start $VMID"
```

## Configure the LAN and enable SSH

Use console menu option 2 to set the LAN address. Decline DHCP, set the address and
subnet, leave the gateway empty, and decline the DHCP server. The console then shows the
web GUI URL and the LAN interface address.

Enable SSH from the shell (menu option 8). Start `/bin/sh` first, because the root shell
is `csh`:

```sh
sh
sysrc openssh_enable=YES
sysrc openssh_skipportscheck=YES
service openssh start
```

Confirm SSH from the Proxmox host before continuing:

```bash
ssh "$PVE" "nc -vz -w 3 $LAN_IP 22"
```

## Enable the guest agent

Install the guest agent, and upgrade the package set first. The OPNsense image ships a
package set frozen at release time, and the mirror ships a current set. Installing against
the frozen set produces an application binary interface (ABI) skew that breaks tools such
as `vtysh`. Running `pkg upgrade -y` first keeps the set self-consistent.

```sh
route add default <gateway> || true
printf 'nameserver 1.1.1.1\n' >/etc/resolv.conf
pkg update -f
pkg upgrade -y
pkg install -y os-qemu-guest-agent
sysrc qemu_guest_agent_enable=YES
service qemu-guest-agent start
```

Confirm the agent answers from the Proxmox host:

```bash
ssh "$PVE" "qm guest exec $VMID -- /bin/hostname"
```

## Generate an API key

OPNsense API access needs a key and secret tied to a user. Generate them with the
`createKey` method, and never echo either value to logs or chat.

```sh
cat > /tmp/create_api_key.php <<'PHP'
<?php
chdir('/usr/local/opnsense/mvc');
require_once('script/load_phalcon.php');
use OPNsense\Auth\API;
$api = new API();
$result = $api->createKey('root');
if ($result === false) { fwrite(STDERR, "createKey returned false\n"); exit(1); }
echo $result['key'] . "\0" . $result['secret'];
PHP
php /tmp/create_api_key.php
```

The output is the key and secret separated by a null byte. Split it into two mode-600
files on the host. Confirm the pair against the firmware status endpoint; a 200 response
means the key is active.

```bash
curl -k -u "$KEY:$SECRET" "https://$LAN_IP/api/core/firmware/status"
```

## Install the daemon

The daemon binary is a published opnsensectl release, not a local build. The
deploy pulls the release the testbed pins, verifies its GitHub attestation, and
unpacks it under `.make/releases/opnsensectl/<tag>/`:

```bash
./configsctl deploy deploy-opnsense --limit opnsense_suburban_servers --check
```

The check run stages the release without touching the guest. Copy
`.make/releases/opnsensectl/<tag>/freebsd_amd64/opnsensectl` onto the guest as
`/usr/local/sbin/mwan-opnsense.current`.

Write the service files, symlink the active slot, and start the daemon. `install`
writes the rc.d script, the run shim, the loader entry, the rc.conf.d settings,
and `/usr/local/etc/opnsensectl.conf`, whose defaults point the daemon at the
named port:

```sh
chmod 0755 /usr/local/sbin/mwan-opnsense.current
/usr/local/sbin/mwan-opnsense.current install
ln -sf /usr/local/sbin/mwan-opnsense.current /usr/local/sbin/mwan-opnsense
sysrc mwan_opnsense_enable=YES
service mwan_opnsense start
service mwan_opnsense status
```

Confirm the named port maps to the device, with the guest agent on its own port:

```text
/dev/vtcon/io.goodkind.mwan-opnsense.0 -> ../ttyV0.1
/dev/vtcon/org.qemu.guest_agent.0 -> ../ttyV1.1
```

## Verify the channel

Run the host bridge on the Proxmox host and probe the daemon. The bridge reads its
upstream and listen sockets from `[opnsense.host]` in `/etc/opnsensectl/config.toml`, and
the probe reads its target from `[opnsense.probe]`.

```bash
opnsensectl host serve --config /etc/opnsensectl/config.toml
opnsensectl daemon version
opnsensectl exec /bin/hostname
```

The version call returns the daemon's build banner, and the exec returns the guest
hostname.

## Validate after a reboot

Reboot the VM and re-run the checks. SSH and the guest agent can be briefly unavailable
right after a reboot even when the serial console already shows the login prompt, so wait
before you judge a failure.

```bash
ssh "$PVE" "qm reboot $VMID"
ssh "$PVE" "qm guest exec $VMID -- /bin/hostname"
opnsensectl daemon version
```

The install is done when the guest agent answers, the daemon reports its version, and the
named port still maps to the device.
