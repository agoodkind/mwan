# Test TCP continuity during native shutdown

Use one isolated hardware graph, one test owner, and a separate management
connection. Record the exact source, SDK, executable, configuration, and input
file hashes before the run. Prepare the SDK with the
[required manual patches](../patches/README.md).

1. Build the Rust examples from the repository root on the selected Linux
   architecture. Set
   `MWAN_NATIVE_DPDK_PREFIX` to the prepared SDK, and export
   `MWAN_DPDK_SOURCE_SHA256` and `MWAN_DPDK_PATCH_SHA256` from its verified
   provenance record.

   ```sh
   PKG_CONFIG_PATH="$MWAN_NATIVE_DPDK_PREFIX/lib/pkgconfig" cargo build --locked --manifest-path native/Cargo.toml --release --features dpdk --example shutdown
   cargo build --locked --manifest-path native/Cargo.toml --release --example conntrack_prepare
   ```

   Set `SHUTDOWN_DRIVER` and `CONNTRACK_PREPARE` to the resulting executables.
   On macOS, run these commands in the existing Linux builder container.

2. Prepare `CONFIG` and `PAIR` using the existing
   [configuration format](malformed/native.conf) and
   [pair format](malformed/tcp4.pair). Select the isolated PCI functions,
   queue resources, and current MAC addresses from the execution manifest.
   Use one pair slot and one TCP direction with identical original and
   translated addresses and ports. Select the server-to-client direction
   that will transmit the downloaded file through the hardware path.

   ```sh
   "$SHUTDOWN_DRIVER" --check-continuity-fixtures "$CONFIG" "$PAIR" 3 5 20
   ```

3. Start one paced download over the prepared route. Fix the client's address
   and source port, disable retries, and keep the connection active beyond
   the driver's final close. Three cycles contain six five-second traffic
   phases. Use a file and transfer rate that take longer than those phases
   and device setup. Save the HTTP result, connection count, socket identity,
   and downloaded file separately from the driver output.

4. On the forwarding host, set `KERNEL_PID` to a live process in the Linux routing namespace that
   owns the connection. Set `ORIGINAL_CLIENT` and `ORIGINAL_SERVER` to its
   client and server endpoints in `address:port` form. The accelerated
   server-to-client pair is this connection's reply direction.

   ```sh
   readlink "/proc/$KERNEL_PID/ns/net"
   sudo nsenter -t "$KERNEL_PID" -n "$CONNTRACK_PREPARE" --inspect "$ORIGINAL_CLIENT" "$ORIGINAL_SERVER"
   ```

   Match the reported namespace to the selected routing namespace. Require
   zone zero, TCP state 3 (`ESTABLISHED`), the exact original and reverse
   reply tuples, and a nonzero connection ID. Save that ID as `CONNECTION_ID`.
   Preserve the reported flags and window scales for comparison.

5. Prepare that connection before installing its hardware rule. Production
   `NativeRuntime.prepareInstall` performs this operation through Go's
   `ConntrackUpdater.SetLiberal`. The standalone Rust case requires the
   equivalent fixture preparation in the connection's namespace.

   ```sh
   sudo nsenter -t "$KERNEL_PID" -n "$CONNTRACK_PREPARE" --set-liberal "$ORIGINAL_CLIENT" "$ORIGINAL_SERVER" "$CONNECTION_ID"
   ```

   Require the same ID, zone, original and reply tuples, state, and window
   scales afterward. Both TCP flag values must include `0x08`. The helper
   sends only that bit with a `0x08` mask in each direction. Do not change
   the global TCP liberal setting. Kernel updates do not compare the ID
   atomically; run this preparation only while the verified test connection
   remains established and exclusive to the test owner.

6. Run the lifecycle case while the original download continues.

   ```sh
   sudo "$SHUTDOWN_DRIVER" --shutdown-continuity "$CONFIG" "$PAIR" 3 5 20
   ```

   Preserve the BNXT log configuration, matching completion events,
   ring IDs, command sequences, firmware results, and packet-buffer release
   order. Require positive counters for each installed generation, all
   three successful Disable/Enable cycles, and successful final close.
   Disable must retain the owner; final close must release it. Expected
   exit status 2 requires the external acceptance checks below.

7. Confirm that the original client process, socket, and conntrack ID remain
   established after final close. Wait for that connection to finish the
   download without reconnecting. Require the complete expected byte count,
   a successful HTTP result, one connection, and matching original and
   downloaded file hashes. Stop captures, remove only test-owned setup,
   verify hardware removal, and check management and household connectivity.
   Preserve the receipts. This successful case does not prove behavior
   during a firmware timeout; record unavailable failure injection separately.
