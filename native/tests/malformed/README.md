# Test truncated native packets

Use dedicated lab interfaces and a built `mwan-native-hardware` from this
checkout. Build the driver with the SDK required by the
[patch list](../../patches/README.md). Use an external sender and an
external receiver on different physical ports. Run one case at a time.

Each case sends valid data, a packet with TTL or Hop Limit 1, a packet missing
its final four payload bytes, and valid data again. The third packet retains
its declared IP length and checksum bytes. These cases verify packet return,
continued forwarding, and cleanup. They do not establish TCP connections or
measure throughput.

Select a case and its expected hardware byte count.

Cases:

| `CASE` | Translation | Hardware bytes |
| --- | --- | ---: |
| [tcp4](tcp4.pair) | IPv4 TCP address and port translation | 724 |
| [tcp6](tcp6.pair) | IPv6 TCP prefix translation | 388 |
| [udp6](udp6.pair) | IPv6 UDP prefix translation | 340 |

1. Verify the dedicated PCI devices, VF numbers, MAC addresses, allowed CPU,
   and hugepage memory against [native.conf](native.conf). Keep the receiver
   VF in Linux and assign the controller and DATA VFs to `vfio-pci`. Keep
   the input DATA VF on the controller's physical port and the output DATA
   VF on the other physical port. Match the sender
   and receiver Ethernet addresses to the selected pair file. Do not use
   interfaces that serve other clients.

   Run the commands from this directory on each host. Set the required shell
   variables in each terminal before running its command.

   Terminal variables:

   | Variable | Required value |
   | --- | --- |
   | `CASE` | One case from the table above |
   | `RESULTS` | A writable absolute directory for this run's captures and comparison files |
   | `OUTPUT_IF` | The external receiver's Ethernet interface |
   | `KERNEL_PID` | A process in the Linux receiver VF's network namespace |
   | `KERNEL_IF` | The receiver VF's Linux interface name |
   | `NATIVE_DRIVER` | The absolute path to the built hardware driver |
   | `INPUT_IF` | The external sender's Ethernet interface |

   ```sh
   mkdir -p "$RESULTS"
   ```

2. Start the output capture on the external receiver. Leave it running in
   its own terminal.

   ```sh
   sudo tcpdump -i "$OUTPUT_IF" -n -U -s 0 -w "$RESULTS/$CASE-actual-output.pcap" 'ether src 02:57:50:04:03:01 and dst port 15402'
   ```

3. Start the kernel capture on the forwarding host in another terminal.

   ```sh
   sudo nsenter -t "$KERNEL_PID" -n tcpdump -i "$KERNEL_IF" -Q in -n -U -s 0 -w "$RESULTS/$CASE-actual-kernel.pcap" 'ether src 80:69:1a:dc:1d:b2 and ether dst 02:57:50:04:00:00 and dst port 15402'
   ```

4. Start the hardware driver on the forwarding host. Wait for
   `READY requested=1 installed=1` before replaying the input. Keep the SDK's
   runtime libraries available in the driver's environment.

   ```sh
   sudo -E "$NATIVE_DRIVER" native.conf "$CASE.pair" 20 2 2
   ```

5. Replay the case once from the external sender before the driver's
   20-second receive period ends. Use captured packet lengths. Do not add
   `--pktlen`, which overrides them.

   ```sh
   sudo tcpreplay --intf1="$INPUT_IF" --pps=10 --loop=1 "$CASE-input.pcap"
   ```

6. Require four successful sends, direction 0 counters with `valid=1/1`,
   `packets=4`, and the byte count from the case table. Require worker counts
   `forwarded=2`, `returned=2`, `malformed=1`, `pending=0`, and
   `quarantined=0`. Require driver exit status 0 and its final `PASS` result,
   which checks removal, disable/reopen, reinstallation, and final shutdown.
   The unused direction does not need valid counters.

   Stop each capture with Control-C after the driver exits. Require two
   captured packets and zero kernel drops in each capture. Copy both actual
   capture files into `RESULTS` on the comparison host.

7. Compare complete Ethernet frame bytes, excluding capture timestamps.
   Run each command separately on the comparison host. Both `cmp` commands
   must exit 0 without output.

   ```sh
   tshark -r "$RESULTS/$CASE-actual-output.pcap" -T jsonraw -j frame > "$RESULTS/$CASE-actual-output.json"
   jq -r '.[]._source.layers.frame_raw[0]' "$RESULTS/$CASE-actual-output.json" > "$RESULTS/$CASE-actual-output.hex"
   tshark -r "$CASE-expected-output.pcap" -T jsonraw -j frame > "$RESULTS/$CASE-expected-output.json"
   jq -r '.[]._source.layers.frame_raw[0]' "$RESULTS/$CASE-expected-output.json" > "$RESULTS/$CASE-expected-output.hex"
   cmp "$RESULTS/$CASE-actual-output.hex" "$RESULTS/$CASE-expected-output.hex"
   tshark -r "$RESULTS/$CASE-actual-kernel.pcap" -T jsonraw -j frame > "$RESULTS/$CASE-actual-kernel.json"
   jq -r '.[]._source.layers.frame_raw[0]' "$RESULTS/$CASE-actual-kernel.json" > "$RESULTS/$CASE-actual-kernel.hex"
   tshark -r "$CASE-expected-kernel.pcap" -T jsonraw -j frame > "$RESULTS/$CASE-expected-kernel.json"
   jq -r '.[]._source.layers.frame_raw[0]' "$RESULTS/$CASE-expected-kernel.json" > "$RESULTS/$CASE-expected-kernel.hex"
   cmp "$RESULTS/$CASE-actual-kernel.hex" "$RESULTS/$CASE-expected-kernel.hex"
   ```

   Repeat from step 2 with the next case. Preserve the captures and driver
   output for each run. A kernel capture proves delivery to the Linux
   interface; it does not prove that Linux accepts an invalid datagram.
