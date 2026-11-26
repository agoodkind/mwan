# Test the gateway on a BlueField-2

Use an isolated card with a LAN test client, two ISP capture interfaces, and an echo server accessible through both ISPs. Run the candidate ARM64 binary with the card's TOML configuration, network document, and matching YANG schema. Run only one instance of ifmgr on the test bridge.

1. Read `Open_vSwitch.other_config` with `ovs-vsctl get Open_vSwitch . other_config`. DOCA requires `doca-init=true` and `hw-offload=true`. If `hw-offload-ct-unidir-udp-enabled=true`, set it to false during maintenance and restart `ovs-vswitchd`. The daemon reads this connection tracking option at startup. Restarting it interrupts forwarding. Check the new process log for `Conntrack unidirectional UDP offloading is disabled`.
2. Configure the IPv6, static IPv4, and shared IPv4 ISP groups as `type: select`. Give the ISP A bucket weight 200 and the ISP C bucket weight 100. Omit `selection-method` and `selection-parameter` from each group. Keep the ISP-specific actions on each bucket.
3. Start the candidate with `mwan ifmgr --role wan --config "$CONFIG" --network "$NETWORK" --schema-dir "$SCHEMA_DIR"`. Check each group's weights with `ovs-ofctl -O OpenFlow15 dump-groups "$BRIDGE"`. Confirm that the running daemon installed the configured rules.
4. Start simultaneous packet captures on both ISP interfaces. Record the current position in the `ovs-vswitchd` log. From a static IPv4 source, send five independent ping requests to the test server. Require five replies before applying load.
5. Run 20 IPv6 TCP downloads and 20 static IPv4 TCP uploads. Record the socket tuple, translated address, expected byte count, and application result for each transfer. For every socket, require the full byte count and capture packets in both directions on the same ISP. Reject a socket if either capture shows that it changed ISP during the transfer.
6. From the shared IPv4 source, send 10,000 UDP echo requests at 500 new sockets per second. Repeat with 60,000 requests at 5,000 new sockets per second. Put a distinct request identifier in each payload. Count each socket once and require a reply on that socket with the exact request payload. Record the actual rate and matching reply count for each run.
7. After each load interval, repeat the independent static IPv4 ping and require five replies. Inspect all log entries since the recorded position, including rotated files. Fail the run on any new `CQE ERR` line or `ovs_rcu` blocked-thread warning. Save both captures, application receipts, and log intervals with the binary revision.

Run the [hardware evidence verifier](../../../gateway/tools/verify-bluefield.py) from the repository root with Python 3 and TShark. Set `EVIDENCE` to the directory containing the M15 hash and echo test outputs for the candidate.

```sh
gateway/tools/verify-bluefield.py tcp "$EVIDENCE/hash/host/implicit"
gateway/tools/verify-bluefield.py udp-500 "$EVIDENCE/echo/udp_500"
gateway/tools/verify-bluefield.py udp-5000 "$EVIDENCE/echo/udp_5000"
```

The TCP check reads the two ISP captures and requires both endpoints to report 819,200 bytes per transfer. It counts resets before each trial's cleanup timestamp. The UDP checks read the echo client's per-socket receipts, the independent ping results, and the log interval. Save the inode and byte offsets at both ends of each interval. Repeat the interval if the log rotates during capture.

## Collect the verifier inputs

Use synchronized clocks on the capture and test hosts. The verifier accepts these files from any collector that records the indicated values.

| TCP file under `hash/host/implicit` | Required contents |
| --- | --- |
| `vx42.pcap`, `isp.200.pcap` | Capture TCP on the ISP A and ISP C logical interfaces respectively, after tunnel decapsulation or VLAN removal. Start both captures before the first connection. Include both directions on server ports 5202 and 5203. |
| `client_N_v6.log`, `server_N_v6.log`, `client_N_v4.log`, `server_N_v4.log` | Use trial numbers 1 through 20 for N. The receiver writes a line `received=819200` after reading the payload. The sender writes `delivered=819200` after confirming delivery with the receiver. Use IPv6 server port 5202 and client port `20000 + 2*N`. Use IPv4 server port 5203 and client port `20001 + 2*N`. |
| `exit_N.txt` | Write four lines with a numeric process ID, one space, and the process exit status. Use the order IPv6 server, IPv4 server, IPv6 client, IPv4 client. Append the capture end time on line five. |
| `cleanup_start_N.txt` | Record Unix time in seconds with fractional seconds immediately before forcibly closing any sockets left after that trial. Capture timestamps before this point determine the ISP and reset checks. |

For each UDP run, create `peer` and `card` directories under its run directory.

| UDP file | Required contents |
| --- | --- |
| `peer/client_0.csv` through `peer/client_3.csv` | Write one row per attempt under the header `source,source_port,dest_port,send_monotonic_ns,reply_monotonic_ns`. Use distinct socket tuples. Record the reply timestamp only when the socket receives the expected peer address, port, and exact request payload. Write zero for a missing send or matching reply. |
| `peer/ping_before.txt`, `peer/ping_after.txt` | Save the Linux `ping -c 5` output for the independent static IPv4 path. |
| `card/log_start.txt`, `card/log_end.txt` | Save Linux `stat -c '%i %s'` output for the active daemon log before and after load. |
| `card/vswitchd.log` | Copy the bytes from the starting offset through the byte before the ending offset. A zero-byte file is valid only when both snapshots report the same inode and offset. |
