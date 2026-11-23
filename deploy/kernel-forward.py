#!/usr/bin/env python3
"""Linux TCP forwarding for a single public IPv4 address and backend pool."""

import argparse
import ipaddress
import json
import logging
import re
import shutil
import subprocess
import time
from pathlib import Path


TABLE = "backpack_router_kernel"
SLOTS = 1000


def nft(*args, rules=None):
    return subprocess.run(
        ["nft", *args], input=rules, text=True, capture_output=True,
        check=True, timeout=5,
    ).stdout


def apply(rules):
    nft("-f", "-", rules="\n".join(rules) + "\n")


def config(path):
    cfg = json.loads(Path(path).read_text())
    for key in ("public_ip", "source_ip"):
        cfg[key] = str(ipaddress.IPv4Address(cfg[key]))
    for key in ("public_if", "backend_if"):
        if not re.fullmatch(r"[\w.-]{1,15}", cfg[key], re.ASCII):
            raise ValueError(f"invalid interface {key}")
    cfg["backends"] = [str(ipaddress.IPv4Address(ip)) for ip in cfg["backends"]]
    cfg.setdefault("backend_port", 443)
    cfg.setdefault("test_ports", [])
    ports = cfg["public_ports"] + cfg["test_ports"]
    if (len(cfg["backends"]) != len(set(cfg["backends"])) or
            (cfg["test_ports"] and len(cfg["backends"]) != len(cfg["test_ports"])) or not cfg["backends"] or
            not cfg["public_ports"] or len(ports) != len(set(ports)) or
            any(type(port) is not int or not 1 <= port <= 65535 for port in [*ports, cfg["backend_port"]])):
        raise ValueError("invalid backend or port configuration")
    return cfg


def forward_rules(cfg):
    wan, lan = json.dumps(cfg["public_if"]), json.dumps(cfg["backend_if"])
    backend_port = cfg["backend_port"]
    ports = "{ " + ", ".join(map(str, cfg["public_ports"] + cfg["test_ports"])) + " }"
    scope = f"ct status dnat ct original ip daddr {cfg['public_ip']} ct original proto-dst {ports}"
    rules = [f"flush chain ip {TABLE} forward"]
    for direction, match, address in (
        ("in", f"iifname {wan} oifname {lan} tcp dport {backend_port}", "daddr"),
        ("out", f"iifname {lan} oifname {wan} tcp sport {backend_port} ct state established", "saddr"),
    ):
        entries = []
        for index, ip in enumerate(cfg["backends"]):
            counter = f"backend_{index}_{direction}"
            chain = f"count_{index}_{direction}"
            entries.append(f"{ip} : jump {chain}")
            rules += [
                f"add chain ip {TABLE} {chain}",
                f"flush chain ip {TABLE} {chain}",
                f"add rule ip {TABLE} {chain} counter name {counter} accept",
            ]
        rules.append(f"add rule ip {TABLE} forward {match} {scope} "
                     f"ip {address} vmap {{ {', '.join(entries)} }}")
    for ingress, egress in ((lan, wan), (wan, lan)):
        rules.append(f"add rule ip {TABLE} forward iifname {ingress} oifname {egress} "
                     f"ip protocol icmp ct state related {scope} accept")
    return rules


def initialize(cfg):
    if TABLE in [line.split()[-1] for line in nft("list", "tables", "ip").splitlines() if line]:
        return
    public, source = cfg["public_ip"], cfg["source_ip"]
    backend_port = cfg["backend_port"]
    wan, lan = json.dumps(cfg["public_if"]), json.dumps(cfg["backend_if"])
    backends = "{ " + ", ".join(cfg["backends"]) + " }"
    ports = "{ " + ", ".join(map(str, cfg["public_ports"] + cfg["test_ports"])) + " }"
    scope = f"ct status dnat ct original ip daddr {public} ct original proto-dst {ports}"
    rules = [
        f"add table ip {TABLE}",
        f"add chain ip {TABLE} prerouting {{ type nat hook prerouting priority -101; policy accept; }}",
        f"add chain ip {TABLE} dispatch",
        f"add chain ip {TABLE} postrouting {{ type nat hook postrouting priority srcnat; policy accept; }}",
        f"add chain ip {TABLE} forward {{ type filter hook forward priority filter; policy drop; }}",
        f"add rule ip {TABLE} prerouting iifname {wan} ip daddr {public} "
        f"tcp dport {ports} tcp flags syn / fin,syn,rst,ack jump dispatch",
        f"add rule ip {TABLE} postrouting iifname {wan} oifname {lan} "
        f"{scope} ip daddr {backends} tcp dport {backend_port} snat to {source}",
    ]
    for index, ip in enumerate(cfg["backends"]):
        rules += [
            f"add chain ip {TABLE} backend_{index}",
            f"add counter ip {TABLE} backend_{index}_new",
            f"add rule ip {TABLE} backend_{index} meta l4proto tcp "
            f"counter name backend_{index}_new dnat to {ip}:{backend_port}",
        ]
        for direction in ("in", "out"):
            rules.append(f"add counter ip {TABLE} backend_{index}_{direction}")
    rules.extend(forward_rules(cfg))
    nft("-c", "-f", "-", rules="\n".join(rules) + "\n")
    apply(rules)


def dispatch_rules(cfg, share=0, weights=None, test=False):
    rules = [f"flush chain ip {TABLE} dispatch"]
    if test:
        for port, ip in zip(cfg["test_ports"], cfg["backends"]):
            rules.append(f"add rule ip {TABLE} dispatch tcp dport {port} counter dnat to {ip}:{cfg['backend_port']}")
    if share and weights:
        ports = "{ " + ", ".join(map(str, cfg["public_ports"])) + " }"
        entries = []
        start = 0
        cumulative = 0
        total = sum(weights.values())
        for index, ip in enumerate(cfg["backends"]):
            cumulative += weights.get(ip, 0)
            end = round(share * cumulative / total)
            if end > start:
                interval = str(start) if end == start + 1 else f"{start}-{end - 1}"
                entries.append(f"{interval} : jump backend_{index}")
                start = end
        if entries:
            rules.append(f"add rule ip {TABLE} dispatch tcp dport {ports} "
                         f"numgen random mod {SLOTS} vmap {{ {', '.join(entries)} }}")
    return rules


def counter_snapshot():
    objects = json.loads(nft("-j", "list", "counters", "table", "ip", TABLE))["nftables"]
    return {obj["counter"]["name"]: obj["counter"] for obj in objects if "counter" in obj}


def traffic_counters(cfg):
    counts = counter_snapshot()
    return {ip: (counts[f"backend_{index}_in"]["bytes"], counts[f"backend_{index}_out"]["bytes"])
            for index, ip in enumerate(cfg["backends"])}


def counters(cfg):
    return {ip: sum(directions) for ip, directions in traffic_counters(cfg).items()}


def active_connections(cfg):
    command = shutil.which("conntrack")
    if command is None:
        return {}
    result = subprocess.run(
        [command, "-L", "-f", "ipv4", "-p", "tcp", "--state", "ESTABLISHED",
         "--orig-dst", cfg["public_ip"], "--reply-dst", cfg["source_ip"]],
        text=True, capture_output=True, check=True, timeout=5,
    )
    active = dict.fromkeys(cfg["backends"], 0)
    ports = set(cfg["public_ports"] + cfg["test_ports"])
    for line in result.stdout.splitlines():
        sources = re.findall(r"\bsrc=([\d.]+)", line)
        destination = re.search(r"\bdport=(\d+)", line)
        if len(sources) == 2 and sources[1] in active and destination and int(destination[1]) in ports:
            active[sources[1]] += 1
    return active


def publish(cfg, path):
    output = Path(path)
    pending = output.with_suffix(".tmp")
    while True:
        try:
            active = active_connections(cfg)
        except (OSError, subprocess.SubprocessError) as error:
            logging.warning("kernel connection count unavailable: %s", error)
            active = {}
        snapshot = {
            "group": cfg.get("control_group", ""),
            "backends": {f"{ip}:{cfg['backend_port']}": {"rx": rx, "tx": tx,
                          **({"active": active[ip]} if ip in active else {})}
                         for ip, (rx, tx) in traffic_counters(cfg).items()},
        }
        pending.write_text(json.dumps(snapshot), encoding="utf-8")
        pending.chmod(0o644)
        pending.replace(output)
        time.sleep(2)


def proxy_status(cfg):
    result = subprocess.run(
        ["curl", "-fsS", "--max-time", "3", "--unix-socket", cfg["health_socket"],
         "http://localhost/status"], text=True, capture_output=True, check=True, timeout=4,
    )
    return json.loads(result.stdout)


def healthy_backends(cfg):
    addresses = {f"{ip}:{cfg['backend_port']}": ip for ip in cfg["backends"]}
    return {addresses[backend["tcp"]]: backend for backend in proxy_status(cfg)["backends"]
            if backend["tcp"] in addresses and backend["state"] == "healthy"
            and (not cfg.get("control_group") or backend["group"] == cfg["control_group"])
            and backend["enabled"] and not backend["drain"]}


def measure(cfg, seconds):
    def sample():
        cpu = Path("/proc/stat").read_text().splitlines()[0].split()[1:9]
        conntrack = int(Path("/proc/sys/net/netfilter/nf_conntrack_count").read_text())
        return time.monotonic(), list(map(int, cpu)), conntrack, counter_snapshot(), proxy_status(cfg)

    before, cpu_before, ct_before, counters_before, go_before = sample()
    time.sleep(seconds)
    after, cpu_after, ct_after, counters_after, go_after = sample()
    elapsed = after - before

    def delta(name, field):
        change = counters_after[name][field] - counters_before[name][field]
        if change < 0:
            raise ValueError("kernel counters reset during measurement")
        return change

    def mbps(byte_count):
        return round(8 * byte_count / elapsed / 1e6, 2)

    cpu_delta = [new - old for old, new in zip(cpu_before, cpu_after)]
    total = sum(cpu_delta)
    backends = []
    for index, ip in enumerate(cfg["backends"]):
        prefix = f"backend_{index}_"
        backends.append({"address": ip, "new": delta(prefix + "new", "packets"),
                         "rx_mbps": mbps(delta(prefix + "in", "bytes")),
                         "tx_mbps": mbps(delta(prefix + "out", "bytes"))})
    if go_after["started"] != go_before["started"]:
        raise ValueError("Go proxy restarted during measurement")
    result = {
        "sample_seconds": round(elapsed, 2),
        "cpu_busy_percent": round(100 * sum(cpu_delta[i] for i in (0, 1, 2, 5, 6)) / total, 2),
        "cpu_softirq_percent": round(100 * cpu_delta[6] / total, 2),
        "conntrack_entries": [ct_before, ct_after],
        "kernel_backends": backends,
        "go": {"active": go_after["active"], "rejected": go_after["rejected"] - go_before["rejected"],
               "rx_mbps": mbps(go_after["rx"] - go_before["rx"]),
               "tx_mbps": mbps(go_after["tx"] - go_before["tx"])},
    }
    print(json.dumps(result, indent=2))


def run(cfg, share_file):
    previous = counters(cfg)
    rates = dict.fromkeys(previous, 0.0)
    updated = time.monotonic()
    applied = None
    fallback = dispatch_rules(cfg)
    while True:
        try:
            try:
                share = int(Path(share_file).read_text().strip())
            except FileNotFoundError:
                share = SLOTS
            if not 0 <= share <= SLOTS:
                raise ValueError("traffic share must be between 0 and 1000")
            health = healthy_backends(cfg)
            current = counters(cfg)
            now = time.monotonic()
            for ip, count in current.items():
                interval_rate = max(0, count - previous[ip]) / max(1, now - updated)
                rates[ip] = 0.7 * rates[ip] + 0.3 * interval_rate
            previous, updated = current, now
            # Per-backend traffic includes connections still draining through Go.
            loads = {ip: rates[ip] + float(health[ip]["rxRate"] or 0) + float(health[ip]["txRate"] or 0)
                     for ip in rates if ip in health}
            average = sum(loads.values()) / len(loads) if loads else 0
            weights = {ip: max(1, round(20 * max(1, health[ip]["weight"]) * (average + 1_000_000) /
                                        (load + average + 1_000_000))) for ip, load in loads.items()}
            plan = dispatch_rules(cfg, share, weights)
            if plan != applied:
                apply(plan)
                applied = plan
                logging.info("traffic slots=%d weights=%s", share, weights)
        except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
            logging.warning("reverting new flows to Go: %s", getattr(error, "stderr", None) or error)
            if applied != fallback:
                apply(fallback)
                applied = fallback
        time.sleep(5)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("config")
    parser.add_argument("command", choices=["check", "init", "tune", "test", "disable", "run", "counters", "publish", "measure"])
    parser.add_argument("--share-file", default="/etc/backpack-router/kernel-forward.share")
    parser.add_argument("--metrics-file", default="/run/backpack-router-kernel/traffic.json")
    parser.add_argument("--seconds", type=int, default=15)
    args = parser.parse_args()
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s")
    cfg = config(args.config)
    if args.command == "check":
        return
    if args.command == "disable":
        apply(dispatch_rules(cfg))
    elif args.command == "publish":
        publish(cfg, args.metrics_file)
    elif args.command == "measure":
        if not 1 <= args.seconds <= 300:
            parser.error("--seconds must be between 1 and 300")
        measure(cfg, args.seconds)
    else:
        initialize(cfg)
        if args.command == "tune":
            rules = forward_rules(cfg)
            nft("-c", "-f", "-", rules="\n".join(rules) + "\n")
            apply(rules)
        elif args.command == "test":
            apply(dispatch_rules(cfg, test=True))
        elif args.command == "run":
            run(cfg, args.share_file)
        elif args.command == "counters":
            print(json.dumps(counters(cfg)))


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as error:
        raise SystemExit(error.stderr or str(error)) from error
