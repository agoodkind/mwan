#!/usr/bin/env python3
# This script checks artifacts from a BlueField test run. It compares TCP
# endpoint receipts with packet captures, checks UDP echoes and the offered
# send rate, and reads a recorded log interval. TCP verification invokes tshark
# on saved captures.

from __future__ import annotations

import argparse
import csv
import math
import re
import subprocess
import sys
from ipaddress import IPv4Address, ip_address
from pathlib import Path

TRIALS = range(1, 21)
SERVER_PORTS = (5202, 5203)
CAPTURES = ("vx42.pcap", "isp.200.pcap")
TRANSFER_BYTES = 819200
SOCKET_ADDRESS = re.compile(r"\('([^']+)', (\d+)(?:, \d+, \d+)?\)")


def receipt_addresses(data: str, event: str) -> list[tuple[str, int]]:
    lines = [line for line in data.splitlines() if line.startswith(event + "=")]
    if len(lines) != 1:
        raise ValueError(f"expected one {event} entry in endpoint receipt")
    return [
        (str(ip_address(address)), int(port))
        for address, port in SOCKET_ADDRESS.findall(lines[0])
    ]


def verify_tcp(directory: Path) -> None:
    cleanup: dict[int, float] = {}
    paths: dict[tuple[int, str], set[str]] = {}
    failures: set[str] = set()
    addresses: dict[int, tuple[str, str]] = {}
    for trial in TRIALS:
        deadline = float((directory / f"cleanup_start_{trial}.txt").read_text())
        if not math.isfinite(deadline) or deadline <= 0:
            raise ValueError(f"invalid cleanup time for trial {trial}")
        exits = (directory / f"exit_{trial}.txt").read_text().splitlines()
        if len(exits) != 5 or not all(re.fullmatch(r"\d+ 0", line) for line in exits[:4]):
            failures.add(f"trial {trial} did not complete on both endpoints")
        for index, family in enumerate(("v6", "v4")):
            port = 20000 + trial * 2 + index
            cleanup[port] = deadline
            client = (directory / f"client_{trial}_{family}.log").read_text()
            server_log = (directory / f"server_{trial}_{family}.log").read_text()
            local, target = receipt_addresses(client, "connect")
            (listener,) = receipt_addresses(server_log, "listening")
            (peer,) = receipt_addresses(server_log, "accepted")
            if (
                local[1] != port
                or peer[1] != port
                or target != listener
                or target[1] != SERVER_PORTS[index]
            ):
                raise ValueError(f"inconsistent endpoint receipts for port {port}")
            for address, _ in (local, peer, listener):
                if f"v{ip_address(address).version}" != family:
                    raise ValueError(f"wrong IP family in receipt for port {port}")
            addresses[port] = peer[0], listener[0]
            for endpoint in ("client", "server"):
                receipt = directory / f"{endpoint}_{trial}_{family}.log"
                label = "received"
                if (family == "v6") == (endpoint == "server"):
                    label = "delivered"
                counts = re.findall(rf"^{label}=(\d+)$", receipt.read_text(), re.M)
                if counts != [str(TRANSFER_BYTES)]:
                    failures.add(f"{receipt.name}: expected {TRANSFER_BYTES} bytes")

    for capture in CAPTURES:
        fields = (
            "frame.time_epoch",
            "tcp.srcport",
            "tcp.dstport",
            "tcp.flags.reset",
            "ip.src",
            "ip.dst",
            "ipv6.src",
            "ipv6.dst",
        )
        command = [
            "tshark",
            "-n",
            "-r",
            str(directory / capture),
            "-Y",
            "tcp.port == 5202 || tcp.port == 5203",
            "-T",
            "fields",
        ]
        for field in fields:
            command.extend(("-e", field))
        if (directory / capture).stat().st_size < 24:
            raise ValueError(f"{capture}: missing capture header")
        result = subprocess.run(command, check=True, stdout=subprocess.PIPE, text=True)
        for line in result.stdout.splitlines():
            epoch, source, destination, reset, source4, target4, source6, target6 = (
                line.split("\t")
            )
            source_port, destination_port = int(source), int(destination)
            if source_port in SERVER_PORTS:
                port, server, direction = destination_port, source_port, "reply"
            else:
                port, server, direction = source_port, destination_port, "request"
            if port not in cleanup or server != SERVER_PORTS[port % 2]:
                raise ValueError(f"{capture}: unexpected socket {source}:{destination}")
            timestamp = float(epoch)
            if not math.isfinite(timestamp) or reset not in ("True", "False", "1", "0"):
                raise ValueError(f"{capture}: invalid TCP metadata")
            if timestamp >= cleanup[port]:
                continue
            packet_addresses = (source4 or source6, target4 or target6)
            if direction == "reply":
                packet_addresses = packet_addresses[::-1]
            if packet_addresses != addresses[port]:
                failures.add(f"{capture}: unexpected IP addresses on port {port}")
                if packet_addresses[1] == addresses[port][1] and reset in ("True", "1"):
                    failures.add(
                        f"{capture}: TCP reset for a different translated address before cleanup on port {port}"
                    )
                continue
            if reset in ("True", "1"):
                failures.add(f"{capture}: TCP reset before cleanup on port {port}")
            paths.setdefault((port, direction), set()).add(capture)

    for port in cleanup:
        requests = paths.get((port, "request"), set())
        replies = paths.get((port, "reply"), set())
        if len(requests) != 1 or requests != replies:
            failures.add(f"port {port}: request ISPs {requests}, reply ISPs {replies}")
    if failures:
        raise ValueError("\n".join(sorted(failures)))
    print("All 40 TCP transfers completed on one ISP in both directions.")


def verify_log(directory: Path) -> None:
    start_inode, start = map(int, (directory / "log_start.txt").read_text().split())
    end_inode, end = map(int, (directory / "log_end.txt").read_text().split())
    interval = (directory / "vswitchd.log").read_bytes()
    if start_inode <= 0 or start_inode != end_inode or start < 0 or end < start:
        raise ValueError("invalid log offsets or rotation during the interval")
    if len(interval) != end - start:
        raise ValueError("log interval size differs from recorded offsets")
    errors = [line for line in interval.splitlines() if b"CQE ERR" in line]
    blocked = re.findall(rb"ovs_rcu[^\n]*blocked", interval)
    if errors or blocked:
        raise ValueError(
            f"{len(errors)} new CQE ERR lines and {len(blocked)} blocked-thread lines"
        )
    print(f"Checked {len(interval)} new ovs-vswitchd log bytes.")


def verify_udp(
    directory: Path,
    expected: int,
    minimum_rate: int,
    shared_sources: set[IPv4Address],
    ping_source: IPv4Address,
    ping_destination: IPv4Address,
) -> None:
    if ping_source in shared_sources:
        raise ValueError("the independent ping requires a separate static IPv4 source")
    observed_sources: set[IPv4Address] = set()
    sockets: set[tuple[str, int, int]] = set()
    send_times: list[int] = []
    directory = directory / "peer"
    for index in range(4):
        previous_count = len(sockets)
        with (directory / f"client_{index}.csv").open(newline="") as handle:
            for row in csv.DictReader(handle):
                source = IPv4Address(row["source"])
                if source not in shared_sources:
                    raise ValueError(f"unexpected shared IPv4 source {source}")
                observed_sources.add(source)
                socket = row["source"], int(row["source_port"]), int(row["dest_port"])
                sent = int(row["send_monotonic_ns"])
                reply = int(row["reply_monotonic_ns"])
                if socket in sockets or sent <= 0 or reply < sent:
                    raise ValueError(
                        f"duplicate socket or missing matching echo: {socket}"
                    )
                sockets.add(socket)
                send_times.append(sent)
        if len(sockets) == previous_count:
            raise ValueError(f"missing echo receipts from client {index}")
    if len(sockets) != expected:
        raise ValueError(f"expected {expected} matching UDP echoes, found {len(sockets)}")
    if observed_sources != shared_sources:
        raise ValueError("missing receipts for a configured shared IPv4 source")
    for phase in ("before", "after"):
        ping = (directory / f"ping_{phase}.txt").read_text()
        header = re.match(r"^PING \S+ \(([^)]+)\) from (\S+)\s*:", ping)
        if not header or (header[1], header[2]) != (
            str(ping_destination),
            str(ping_source),
        ):
            raise ValueError(f"unexpected static IPv4 ping endpoints {phase} load")
        replies = re.findall(r"^\d+ bytes from (\S+): icmp_seq=(\d+) ", ping, re.M)
        if sorted(replies) != [(str(ping_destination), str(seq)) for seq in range(1, 6)]:
            raise ValueError(f"unexpected static IPv4 ping replies {phase} load")
        if not re.search(
            r"^5 packets transmitted, 5 received, 0% packet loss,", ping, re.M
        ):
            raise ValueError(f"independent static IPv4 ping failed {phase} load")
    duration = (max(send_times) - min(send_times)) / 1_000_000_000
    if duration <= 0:
        raise ValueError("missing UDP send interval")
    rate = (expected - 1) / duration
    if rate < minimum_rate:
        raise ValueError(
            f"offered {rate:.1f} UDP sends per second, require {minimum_rate}"
        )
    print(f"Verified {expected} UDP echoes at {rate:.1f} sends per second.")
    print("Independent static IPv4 pings received five replies before and after load.")


def main() -> None:
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="check", required=True)
    for check in ("tcp", "log", "udp-500", "udp-5000"):
        command = commands.add_parser(check)
        command.add_argument("directory", type=Path)
        if check.startswith("udp-"):
            command.add_argument(
                "--shared-source", type=IPv4Address, action="append", required=True
            )
            command.add_argument("--ping-source", type=IPv4Address, required=True)
            command.add_argument("--ping-destination", type=IPv4Address, required=True)
    args = parser.parse_args()
    if args.check == "tcp":
        verify_tcp(args.directory)
    elif args.check == "log":
        verify_log(args.directory)
    else:
        workload = {"udp-500": (10000, 500), "udp-5000": (60000, 5000)}
        verify_udp(
            args.directory,
            *workload[args.check],
            set(args.shared_source),
            args.ping_source,
            args.ping_destination,
        )
        verify_log(args.directory / "card")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))
