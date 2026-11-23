#!/usr/bin/env python3
"""Prepare a fresh BackPack Router with a Linux TCP data plane."""

import argparse
import ipaddress
import json
import re
import subprocess
from pathlib import Path


def port(value):
    number = int(value)
    if not 1 <= number <= 65535:
        raise argparse.ArgumentTypeError("port must be between 1 and 65535")
    return number


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    parser.add_argument("--public-interface", required=True)
    parser.add_argument("--public-ip", required=True, type=ipaddress.IPv4Address)
    parser.add_argument("--private-interface", required=True)
    parser.add_argument("--private-ip", required=True, type=ipaddress.IPv4Address)
    parser.add_argument("--port", required=True, type=port)
    parser.add_argument("--backend-port", type=port)
    parser.add_argument("--backend", required=True, action="append", type=ipaddress.IPv4Address)
    args = parser.parse_args()

    interfaces = (args.public_interface, args.private_interface)
    if interfaces[0] == interfaces[1] or any(
        not re.fullmatch(r"[\w.-]{1,15}", name, re.ASCII) for name in interfaces
    ):
        parser.error("expected two distinct Linux interface names")
    if args.public_ip == args.private_ip or args.public_ip.is_unspecified or args.public_ip.is_multicast:
        parser.error("expected a distinct unicast public address")
    networks = tuple(map(ipaddress.IPv4Network, ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")))
    if any(not any(ip in network for network in networks) for ip in (args.private_ip, *args.backend)):
        parser.error("the private router address and backends must use RFC 1918 IPv4 addresses")
    if (len(set(args.backend)) != len(args.backend) or len(args.backend) > 32
            or args.private_ip in args.backend or args.public_ip in args.backend):
        parser.error("expected up to 32 distinct backend addresses, separate from the router")
    current = json.loads(subprocess.check_output(["ip", "-j", "-4", "address", "show"], text=True))
    assigned = {item["ifname"]: {info["local"] for info in item["addr_info"]} for item in current}
    for interface, address in zip(interfaces, (args.public_ip, args.private_ip)):
        if str(address) not in assigned.get(interface, set()):
            parser.error(f"{address} is not assigned to {interface}")

    defaults = Path(__file__).resolve().parent
    group = "kernel-tcp"
    public, private = str(args.public_ip), str(args.private_ip)
    backend_port = args.backend_port or args.port
    destinations = list(map(str, args.backend))
    engine = json.loads((defaults / "engine.example.json").read_text(encoding="utf-8"))
    engine["groups"] = [{
        "id": group, "tcp": f"{private}:{args.port}", "udp": "",
        "backends": [
            {"id": f"backend-{index}", "name": ip, "tcp": f"{ip}:{backend_port}", "enabled": True, "weight": 1}
            for index, ip in enumerate(destinations, 1)
        ],
    }]
    kernel = {
        "public_if": interfaces[0], "backend_if": interfaces[1], "public_ip": public, "source_ip": private,
        "public_ports": [args.port], "backend_port": backend_port, "backends": destinations,
        "control_group": group, "health_socket": "/run/backpack-router/control.sock",
    }
    template = (defaults / "native-tcp-ingress.nft.example").read_text(encoding="utf-8")
    values = {"eth0": interfaces[0], "203.0.113.10": public, "10.0.0.2": private, "8443": str(args.port)}
    rules = re.sub("|".join(map(re.escape, values)), lambda match: values[match[0]], template)
    for name, config in (("engine.json", engine), ("kernel-forward.json", kernel)):
        (args.output / name).write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")
    (args.output / "native-tcp-ingress.nft").write_text(rules, encoding="utf-8")


if __name__ == "__main__":
    main()
