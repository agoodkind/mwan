#!/usr/bin/env python3
from __future__ import annotations

import argparse
import csv
import math
import re
import subprocess
import sys
from pathlib import Path

TRIALS = range(1, 21)
SERVER_PORTS = (5202, 5203)
CAPTURES = ("vx42.pcap", "isp.200.pcap")
TRANSFER_BYTES = 819200


def verify_tcp(directory: Path) -> None:
    cleanup: dict[int, float] = {}
    paths: dict[tuple[int, str], set[str]] = {}
    failures: set[str] = set()
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
            "ip.version",
            "ipv6.version",
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
        result = subprocess.run(command, check=True, stdout=subprocess.PIPE, text=True)
        if not result.stdout.strip():
            raise ValueError(f"{capture}: missing TCP evidence")
        for line in result.stdout.splitlines():
            epoch, source, destination, reset, ipv4, ipv6 = line.split("\t")
            source_port, destination_port = int(source), int(destination)
            if source_port in SERVER_PORTS:
                port, server, direction = destination_port, source_port, "reply"
            else:
                port, server, direction = source_port, destination_port, "request"
            if port not in cleanup or server != SERVER_PORTS[port % 2]:
                raise ValueError(f"{capture}: unexpected socket {source}:{destination}")
            if (port % 2 == 0 and ipv6 != "6") or (port % 2 == 1 and ipv4 != "4"):
                raise ValueError(f"{capture}: unexpected IP family for port {port}")
            timestamp = float(epoch)
            if not math.isfinite(timestamp) or reset not in ("True", "False", "1", "0"):
                raise ValueError(f"{capture}: invalid TCP metadata")
            if timestamp >= cleanup[port]:
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


def verify_udp(directory: Path, expected: int) -> None:
    sockets: set[tuple[str, int, int]] = set()
    send_times: list[int] = []
    directory = directory / "peer"
    for index in range(4):
        previous_count = len(sockets)
        with (directory / f"client_{index}.csv").open(newline="") as handle:
            for row in csv.DictReader(handle):
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
    for phase in ("before", "after"):
        ping = (directory / f"ping_{phase}.txt").read_text()
        if not re.search(
            r"^5 packets transmitted, 5 received, 0% packet loss,", ping, re.M
        ):
            raise ValueError(f"independent static IPv4 ping failed {phase} load")
    duration = (max(send_times) - min(send_times)) / 1_000_000_000
    if duration <= 0:
        raise ValueError("missing UDP send interval")
    print(f"Verified {expected} UDP echoes at {expected / duration:.1f} sends per second.")
    print("Independent static IPv4 pings received five replies before and after load.")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("check", choices=("tcp", "log", "udp-500", "udp-5000"))
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    if args.check == "tcp":
        verify_tcp(args.directory)
    elif args.check == "log":
        verify_log(args.directory)
    else:
        expected = {"udp-500": 10000, "udp-5000": 60000}
        verify_udp(args.directory, expected[args.check])
        verify_log(args.directory / "card")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))
