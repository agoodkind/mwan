> Read the [repository context](../../README.md) before using copied commands or historical plans.

# AT&T ONT and 802.1X

The AT&T line terminates on an optical network terminal that the MWAN VM reaches as a Layer 2 device, and the line authenticates with 802.1X before it passes traffic. This page covers reaching the terminal and the authentication bring-up chain.

## ONT access

The AT&T optical network terminal, the device that ends the fiber, is a Realtek GPON module that MWAN sees as a Layer 2 device on the untagged `enatt0` interface. MWAN reaches its management plane at `192.168.1.1`.

The terminal keeps the stock vendor defaults for this Humax module, and those credentials are not stored in the repo, so get them from the operator password manager. It runs dropbear 0.48, which requires legacy key-exchange, host-key, and cipher options, and it also exposes telnet on port 23.

Reach it over SSH from the MWAN VM, replacing `<user>` and supplying the password when prompted.

```bash
ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  -o KexAlgorithms=+diffie-hellman-group1-sha1 \
  -o HostKeyAlgorithms=+ssh-rsa \
  -o Ciphers=+3des-cbc \
  <user>@192.168.1.1
```

The terminal exposes diagnostic tools such as `ShowStatus`, `pondetect`, `checkomci`, `omci_app`, `omcicli`, and `oamcli`, and status files under `/proc` such as `/proc/omci`, `/proc/fiber_debug`, `/proc/fiber_mode`, and `/proc/internet_flag`. It accepts only a few connections at once and wedges its SSH and telnet daemons when hit rapidly, so keep to one connection at a time. If a daemon wedges but SSH still answers, reboot the terminal over SSH; if SSH itself stops answering, power-cycle the terminal to recover.

## 802.1X authentication

AT&T authenticates the line with 802.1X, so the MWAN VM holds AT&T's certificates and runs `wpa_supplicant` directly while the terminal forwards the authentication frames at Layer 2. The bring-up runs as a chain of systemd units.

1. `wpa_supplicant-mwan.service` runs the supplicant against `enatt0` using the certificates in `/etc/wpa_supplicant/`.
2. When the supplicant reaches the authenticated state, `wpa-cli-action.service` writes the marker file `/run/wpa_supplicant-mwan.authenticated`.
3. `wpa-authenticated.path` watches that marker and starts `wpa-authenticated.service`, which starts `bringup-att-vlan.service`.
4. `bringup-att-vlan.sh` waits for authentication, then runs `networkctl renew enatt0.3242` to trigger DHCPv4 and DHCPv6 prefix delegation on the VLAN.
5. DHCPv4 yields the AT&T public address, and DHCPv6 yields the delegated IPv6 prefix that the network-prefix-translation rules map onto.

The failures you meet, with their recovery, are these.

- `wpa-authenticated.path` sits in `failed (unit-start-limit-hit)` when the path started `bringup-att-vlan` too many times at boot. Clear it with `systemctl reset-failed wpa-authenticated.path`, then `systemctl start wpa-authenticated.path`.
- A completed TLS handshake followed by `EAP-Failure` means AT&T's authentication server rejected the certificate identity. The supplicant port can stay authorized from the boot-time authentication while DHCP gets no replies, because the authentication session is invalid. Reboot the terminal over SSH to clear AT&T's session state; it returns in about thirty seconds and re-presents its authentication frames, which starts a fresh session.
- `Supplicant PAE state=AUTHENTICATING` with `wpa_state=COMPLETED` means re-authentication is failing in the background, and the same terminal reboot recovers it.

Inspect the chain on the MWAN VM.

```bash
ls -la /run/wpa_supplicant-mwan.authenticated
ls -la /etc/wpa_supplicant/*.pem
sudo wpa_cli -i enatt0 status
journalctl -u wpa_supplicant-mwan --no-pager --since "1 hour ago"
journalctl -u bringup-att-vlan --no-pager --since "1 hour ago"
```
