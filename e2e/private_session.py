#!/usr/bin/env python3
"""Run the locally built VM images on real playground VMs, with nothing published.

A playground can only boot a rootfs from a public registry. This starts a private session from
the stock Ubuntu image instead, copies each built image's own layers onto its VM, and reboots it,
so the VM comes up as if it had booted that image. Adapted from Break Team's tool.

  make images                                          # build growingpains-{node,ops}-rootfs:dev
  python3 e2e/private_session.py start                 # prints a session id
  python3 e2e/private_session.py update <id> ops       # after rebuilding one image
  labctl playground destroy <id>                       # when done

It cannot check what depends on the published image itself: the platform's pull time and cache.
"""

import argparse
import json
import subprocess
import sys
import tarfile
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "playground"))

import yaml  # noqa: E402

import render_manifest  # noqa: E402

REGISTRY = "ghcr.io/lpmi-13"
BASE = (
    "ghcr.io/iximiuz/labs/rootfs:ubuntu-26-04"
    "@sha256:9be358de5a0eee258adf110f6fd33f1dfea39acf71d17754920c954a83563145"
)
TARGETS = {"ops-01": "ops", "node-01": "node", "node-02": "node", "node-03": "node", "node-04": "node"}
WHITEOUT = ".wh."
OPAQUE = ".wh..wh..opq"
SKIPPED_TOP_LEVEL = ("tmp", "run", "proc", "sys", "dev")
# Prometheus stores the run's judge history here. The ops image marks this directory opaque;
# deleting it during an overlay update would erase completed-run evidence and leave the narrator
# with an incomplete report after reboot. Tar extraction preserves existing child files.
PRESERVE_RUNTIME = ("var/lib/prometheus",)


def run(*args, stdin=None, timeout=1800):
    return subprocess.run(args, check=True, capture_output=True, text=True, input=stdin, timeout=timeout).stdout


def layer_count(image):
    # A BuildKit build keeps its base in the build cache, not the image store, so pull it.
    if subprocess.run(["docker", "image", "inspect", image], capture_output=True).returncode != 0:
        run("docker", "pull", "-q", image)
    return int(run("docker", "inspect", "--format", "{{len .RootFS.Layers}}", image).strip())


def skipped(path):
    return path.split("/", 1)[0] in SKIPPED_TOP_LEVEL


def merged_overlay(image, out):
    """Flatten the layers above the base into one archive. Returns what they remove from it."""
    base = layer_count(BASE)
    with tempfile.TemporaryDirectory(dir=out.parent) as tmp:
        archive = Path(tmp) / "image.tar"
        run("docker", "save", "-o", str(archive), image)
        with tarfile.open(archive) as saved:
            names = json.load(saved.extractfile("manifest.json"))[0]["Layers"][base:]
            saved.extractall(tmp, members=[saved.getmember(n) for n in names], filter="tar")

        # Top layer first, so the newest version of each path wins.
        winners, hidden, removed = {}, set(), []
        covered = lambda path: any(path == h or path.startswith(h + "/") for h in hidden)
        for index in reversed(range(len(names))):
            layer_hidden = set()
            with tarfile.open(Path(tmp) / names[index]) as layer:
                for member in layer:
                    path = member.name.lstrip("./").rstrip("/")
                    if not path or skipped(path) or covered(path):
                        continue
                    folder, _, leaf = path.rpartition("/")
                    if leaf == OPAQUE:
                        layer_hidden.add(folder)
                        removed.append((folder, True))
                    elif leaf.startswith(WHITEOUT):
                        target = f"{folder}/{leaf[len(WHITEOUT):]}".lstrip("/")
                        layer_hidden.add(target)
                        if target not in winners:
                            removed.append((target, False))
                    elif path not in winners:
                        winners[path] = index
            hidden |= layer_hidden

        # Bottom layer first, in each layer's own order, so hard link targets come first.
        with subprocess.Popen(["pigz", "-1", "-c"], stdin=subprocess.PIPE, stdout=out.open("wb")) as pigz:
            with tarfile.open(fileobj=pigz.stdin, mode="w|", format=tarfile.PAX_FORMAT) as merged:
                for index, name in enumerate(names):
                    with tarfile.open(Path(tmp) / name) as layer:
                        for member in layer:
                            path = member.name.lstrip("./").rstrip("/")
                            if winners.get(path) == index:
                                merged.addfile(member, layer.extractfile(member) if member.isreg() else None)
            pigz.stdin.close()
    return removed


def apply_overlay(play, vm, archive, removed):
    """Copy a prepared overlay onto a VM and unpack it over the root filesystem."""
    size = archive.stat().st_size / 1024 / 1024
    built = time.monotonic()
    run("labctl", "cp", "--machine", vm, "--user", "root", str(archive), f"{play}:/var/tmp/overlay.tar.gz")
    copied = time.monotonic()
    # The node image carries the seeded database. Unpacked over a cluster that has run, its old
    # pg_control and files mix with newer WAL and the cluster cannot start ("could not locate a
    # valid checkpoint record"). Lay the image's cluster down whole: the first-boot database.
    commands = [
        "systemctl stop postgresql@18-main gp-pg-replica 2>/dev/null",
        "[ -d /var/lib/postgresql/18/main ] && rm -rf /var/lib/postgresql/18/main",
        "true",
    ]
    for path, opaque in removed:
        if any(path == keep or path.startswith(keep + "/") for keep in PRESERVE_RUNTIME):
            continue
        quoted = "'/" + path.replace("'", "'\\''") + "'"
        commands.append(f"rm -rf {quoted}/* {quoted}/.[!.]* 2>/dev/null" if opaque else f"rm -rf {quoted}")
    commands.append(
        "tar --warning=no-timestamp --xattrs --xattrs-include='*' --numeric-owner --same-owner "
        "-xpzf /var/tmp/overlay.tar.gz -C / ; echo tar-exit=$?"
    )
    commands.append("rm -f /var/tmp/overlay.tar.gz; sync")
    output = run(
        "labctl", "ssh", play, "--machine", vm, "--user", "root", "--", "bash", "-s",
        stdin="\n".join(commands) + "\n",
    )
    if "tar-exit=0" not in output:
        print(f"    {vm} extraction: {output[-600:]}", flush=True)
    print(f"    {vm}: {size:.0f} MiB overlay, copied in {copied - built:.0f}s, applied in {time.monotonic() - copied:.0f}s", flush=True)



def wait_cluster_ready(play, timeout=300):
    """Wait for kubectl on the rebooted ops VM and all five nodes to be ready."""
    deadline = time.monotonic() + timeout
    expected = set(TARGETS)
    last = "kubectl unavailable"
    while time.monotonic() < deadline:
        try:
            result = subprocess.run(
                ["labctl", "ssh", play, "--machine", "ops-01", "--user", "root", "--",
                 "kubectl", "get", "nodes", "-o", "json"],
                capture_output=True, text=True, timeout=30,
            )
            if result.returncode == 0:
                nodes = json.loads(result.stdout)["items"]
                ready = {node["metadata"]["name"] for node in nodes
                         if any(c["type"] == "Ready" and c["status"] == "True"
                                for c in node["status"].get("conditions", []))}
                if expected <= ready:
                    print(f"    cluster ready: {', '.join(sorted(expected))}", flush=True)
                    return
                last = f"ready nodes: {', '.join(sorted(ready)) or 'none'}"
            else:
                last = result.stderr.strip()[-300:] or "kubectl unavailable"
        except (subprocess.TimeoutExpired, ValueError, KeyError) as error:
            last = str(error)[-300:]
        time.sleep(5)
    raise RuntimeError(f"cluster did not become ready within {timeout}s: {last}")


def start(free_tier):
    with tempfile.TemporaryDirectory() as tmp:
        path = Path(tmp) / "manifest.yaml"
        path.write_text(yaml.safe_dump(render_manifest.build("growing-pains-private", "unused", private=True), sort_keys=False))
        tier = ["--as-free-tier-user"] if free_tier else []
        play = run(
            "labctl", "playground", "start", "flexbox", "-f", str(path), *tier,
            "--title", "growingpains-private", "--quiet", "--safety-disclaimer-consent",
        ).strip().split()[-1]
    print(f"session {play}", flush=True)
    update(play, list(TARGETS))
    print(play)


def update(play, vms):
    work = ROOT / "e2e" / ".logs"
    work.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(dir=work) as tmp:
        overlays = {}
        for target in sorted({TARGETS[vm] for vm in vms}):
            started = time.monotonic()
            archive = Path(tmp) / f"{target}.tar.gz"
            removed = merged_overlay(f"{REGISTRY}/growingpains-{target}-rootfs:dev", archive)
            overlays[target] = (archive, removed)
            print(f"    {target}: overlay built in {time.monotonic() - started:.0f}s", flush=True)
        for vm in vms:
            apply_overlay(play, vm, *overlays[TARGETS[vm]])
    for vm in vms:
        run("labctl", "playground", "machine", "reboot", play, vm, timeout=300)
    print(f"    rebooted {', '.join(vms)}", flush=True)
    wait_cluster_ready(play)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    commands = parser.add_subparsers(dest="command", required=True)
    starting = commands.add_parser("start")
    starting.add_argument("--free-tier", action="store_true", help="start as a free-tier user (halves every capacity unit)")
    updating = commands.add_parser("update")
    updating.add_argument("session")
    updating.add_argument("vms", nargs="+", help="VM names, or an image target: ops or node")
    args = parser.parse_args()
    if args.command == "start":
        start(args.free_tier)
    else:
        vms = []
        for v in args.vms:
            vms += [vm for vm, t in TARGETS.items() if t == v] if v in ("ops", "node") else [v]
        update(args.session, vms)


if __name__ == "__main__":
    main()
