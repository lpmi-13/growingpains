#!/usr/bin/env python3
"""Render the Growing Pains playground manifest.

    python3 playground/render_manifest.py --tag v1 --output playground/manifest.yaml
    python3 playground/render_manifest.py --private --output /tmp/m.yaml   # stock images, for e2e/private_session.py

One premium playground: 10 vCPU and 16 GiB across five VMs (plan, "Topology").
"""

import argparse
import copy
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
REGISTRY = "ghcr.io/lpmi-13"
SUBNET = "172.16.0.0/24"

# (name, image target, vCPUs, RAM, address, disk)
VMS = (
    ("ops-01", "ops", 2, "4GiB", "172.16.0.10/24", "16GiB"),
    ("node-01", "node", 2, "3GiB", "172.16.0.11/24", "16GiB"),
    ("node-02", "node", 2, "3GiB", "172.16.0.12/24", "12GiB"),
    ("node-03", "node", 2, "3GiB", "172.16.0.13/24", "12GiB"),
    ("node-04", "node", 2, "3GiB", "172.16.0.14/24", "16GiB"),
)

WELCOME = {
    "ops-01": "ops-01: the outside world and the cloud provider. k3s control plane, Prometheus, Grafana, the architect, the crowd, and the prober.",
    "node-01": "node-01: the whole shop at genesis. nginx, the API process, PostgreSQL 18.",
}

DESCRIPTION = "A tiny web shop grows itself under real, rising traffic. An autonomous architect reads its telemetry and makes the change an experienced engineer would make."

MARKDOWN = """\
One tiny shop. The traffic decides how it grows.

1. Start the playground and open the **Narrator** and **Grafana** tabs.
2. The run starts on its own after three minutes, or press **Start** on the narrator page.
3. Watch traffic, errors, latency, and SLOs while the architect grows the shop, and read why.
"""

WAIT_FOR_GRAFANA = "#!/bin/bash\ncurl -fsS -m 3 http://127.0.0.1:3000/api/health >/dev/null\n"


def machine(name, target, cpus, ram, address, disk, tag, digests, private):
    user = {"name": "laborant", "default": True}
    if name in WELCOME and not private:
        user["welcome"] = WELCOME[name]
    source = "ubuntu-26-04" if private else f"oci://{REGISTRY}/growingpains-{target}-rootfs:{tag}"
    # The platform caches an image by its tag, so each publish pins the digest it pushed.
    if digests and not private:
        source += f"@{digests[target]}"
    return {
        "name": name,
        "users": [{"name": "root"}, user],
        "drives": [{"source": source, "mount": "/", "size": disk}],
        "network": {"interfaces": [{"address": address, "network": "local"}]},
        "resources": {"cpuCount": cpus, "ramSize": ram},
    }


def build(name, tag, digests=None, private=False):
    tabs = [
        {"id": "narrator", "kind": "http-port", "name": "Narrator", "machine": "ops-01", "number": 8080, "access": "public"},
        {"id": "grafana", "kind": "http-port", "name": "Grafana", "machine": "ops-01", "number": 3000, "access": "public"},
        {"id": "architect", "kind": "http-port", "name": "Architect", "machine": "ops-01", "number": 7000},
        {"id": "shop", "kind": "http-port", "name": "Shop", "machine": "node-01", "number": 80},
        {"id": "terminal-ops-01", "kind": "terminal", "name": "ops-01", "machine": "ops-01"},
        {"id": "terminal-node-01", "kind": "terminal", "name": "node-01", "machine": "node-01"},
    ]
    who = ["owner"] if private else ["anyone"]
    pg = {
        "networks": [{"name": "local", "subnet": SUBNET}],
        "machines": [machine(*vm, tag, digests, private) for vm in VMS],
        "tabs": tabs if not private else [t for t in tabs if t["kind"] == "terminal"],
        "accessControl": {"canList": list(who), "canRead": list(who), "canStart": list(who)},
    }
    if not private:
        # The platform retries a failing init task every second, so one attempt is a poll.
        pg["initTasks"] = {"wait_for_grafana": {"name": "wait_for_grafana", "machine": "ops-01", "init": True,
                                                "user": "root", "timeout_seconds": 300, "run": WAIT_FOR_GRAFANA}}
    return {
        "kind": "playground", "name": name, "base": "flexbox", "title": "Growing Pains",
        "description": DESCRIPTION, "categories": ["kubernetes", "observability", "linux"], "markdown": MARKDOWN,
        "playground": pg,
    }


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--name", default="growing-pains")
    p.add_argument("--tag", default="dev")
    p.add_argument("--digest", action="append", default=[], metavar="TARGET=DIGEST")
    p.add_argument("--private", action="store_true", help="stock images, owner only, for private sessions")
    p.add_argument("--output", type=Path, default=ROOT / "playground" / "manifest.yaml")
    args = p.parse_args()
    digests = dict(d.split("=", 1) for d in args.digest)
    manifest = build(args.name if not args.private else "growing-pains-private", args.tag, digests, args.private)
    args.output.write_text(yaml.safe_dump(manifest, sort_keys=False))
    print(f"wrote {args.output}")


if __name__ == "__main__":
    main()
