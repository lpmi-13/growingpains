#!/usr/bin/env python3
"""Shrink a running playground back to genesis, so another run can start without a new session.

    python3 e2e/reset_genesis.py <session id> [--observe]

It undoes every MVP move: the cache and the fleet (Kubernetes objects in `shop`), the replica
(the unit on node-04, any quota lift from e2e/storyline_run.py, and the primary's slot), any
pooler or CPU-cap change, the node taints, and the edge. The node image includes a clean, seeded
PostgreSQL cluster; reset verifies its checksum and restores it after stopping the primary, so
each run starts with the same rows and review counts. Then it restarts the genesis API, clears
the architect's and the crowd's state, and checks the result.

`--observe` starts the architect with `-observe` (the control run: it measures and arms, and
never moves), through a drop-in under /run that a reboot also clears. Without it, the drop-in is
removed and the architect acts.

It opens one session per machine step, four in all (e2e/lab.py counts them). The plan's simpler
choice for repeat runs is a fresh playground. This is the cheaper one while iterating on a
private session.
"""

import argparse
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import lab  # noqa: E402

GENESIS_UPSTREAM = (lab.ROOT / "deploy" / "nginx" / "upstream-genesis.conf").read_text()
LAB = "gp-crowd gp-prober gp-architect gp-narrator"
OBSERVE_DROPIN = "/run/systemd/system/gp-architect.service.d/observe.conf"
OBSERVE_ENV = "/run/gp-architect-observe.env"


def quota_seconds(value: str) -> float:
    if value.endswith("ms"):
        return float(value[:-2]) / 1000
    if value.endswith("s"):
        return float(value[:-1])
    raise ValueError(f"unknown quota duration: {value}")


def reset(play: str, observe: bool = False, echo=print, delay: str | None = None) -> str:
    """Bring the session back to genesis. Returns the check's report; raises if it is not at genesis."""
    if observe and delay:
        raise ValueError("observe and delay controls are mutually exclusive")
    if delay and not re.fullmatch(r"[1-9][0-9]*(s|m)", delay):
        raise ValueError("delay must be a positive duration such as 90s")
    echo("ops-01: stop the lab and any harness run, delete the shop's objects")
    lab.ssh(play, "ops-01", f"""
systemctl stop {lab.UNIT} 2>/dev/null || true
systemctl stop {LAB}
kubectl -n shop delete deployment,service,hpa,pdb,pod --all --wait=true --timeout=120s >/dev/null
""")

    echo("node-04: remove the replica and any lifted quota")
    replica = lab.ssh(play, "node-04", """
/usr/local/libexec/gp/actions/pg-replica-remove
# storyline_run.py --lift-replica-at lifts the quota with set-property --runtime, under /run.
if [ -d /run/systemd/system.control/gp-pg-replica.service.d ] || [ -d /etc/systemd/system.control/gp-pg-replica.service.d ]; then
  rm -rf /run/systemd/system.control/gp-pg-replica.service.d /etc/systemd/system.control/gp-pg-replica.service.d
  systemctl daemon-reload
fi
echo "replica quota: $(systemctl show -p CPUQuotaPerSecUSec --value gp-pg-replica.service)"
""")

    echo("node-01: restore the seeded database, cap, edge, and API")
    node = lab.ssh(play, "node-01", f"""
sha256sum -c /opt/gp/genesis-db.sha256
runuser -u postgres -- psql -q -d shop -Atc "select pg_drop_replication_slot(slot_name) from pg_replication_slots where not active" >/dev/null
/usr/local/libexec/gp/actions/pgbouncer-stop 2>/dev/null || true
systemctl stop gp-api.service postgresql@18-main.service
rm -rf /var/lib/postgresql/18/main
tar -C /var/lib/postgresql/18 -xzf /opt/gp/genesis-db.tar.gz
systemctl start postgresql@18-main.service
runuser -u postgres -- pg_isready -q -d shop
# pg-cap uses set-property, which writes 50-CPUQuota.conf under system.control; the image's cap
# (10-caps.conf, which sorts before it) stays.
if [ -d /etc/systemd/system.control/postgresql@18-main.service.d ]; then
  systemctl set-property postgresql@18-main.service CPUQuota=65%
  rm -rf /etc/systemd/system.control/postgresql@18-main.service.d
  systemctl daemon-reload
fi
/usr/local/libexec/gp/actions/nginx-apply <<'CONF'
{GENESIS_UPSTREAM}CONF
systemctl enable --now gp-api.service
echo "primary quota: $(systemctl show -p CPUQuotaPerSecUSec --value postgresql@18-main.service)"
echo "gp-api: $(systemctl is-active gp-api)"
echo "slots: $(runuser -u postgres -- psql -d shop -Atc 'select count(*) from pg_replication_slots')"
echo "products: $(runuser -u postgres -- psql -d shop -Atc 'select count(*) from products')"
echo "upstream servers: $(grep -c '^ *server ' /etc/nginx/gp/upstream.conf)"
""")

    mode = "observe" if observe else f"delayed {delay}" if delay else "acting"
    echo(f"ops-01: re-taint the nodes, clear run state, start the lab (architect {mode}), check")
    if observe or delay:
        argument = "-observe" if observe else f"-delay={delay}"
        architect_args = f"""mkdir -p {Path(OBSERVE_DROPIN).parent}
printf 'GP_ARCHITECT_ARGS={argument}\\n' > {OBSERVE_ENV}
printf '[Service]\\nEnvironmentFile={OBSERVE_ENV}\\n' > {OBSERVE_DROPIN}"""
    else:
        architect_args = f"rm -f {OBSERVE_DROPIN} {OBSERVE_ENV}"
    report = lab.ssh(play, "ops-01", f"""
for n in node-02 node-03 node-04; do
  kubectl taint nodes $n growing-pains/data- >/dev/null 2>&1 || true
  kubectl taint nodes $n growing-pains/dormant=true:NoSchedule --overwrite >/dev/null
done
{architect_args}
systemctl daemon-reload
rm -rf /var/lib/gp-architect/* /var/lib/private/gp-crowd/*
systemctl start {LAB}
echo "shop objects: $(kubectl -n shop get all --no-headers 2>/dev/null | wc -l)"
kubectl get nodes --no-headers -o custom-columns=N:.metadata.name,T:.spec.taints[*].key | sed 's/^/taints: /'
for u in {LAB}; do echo "$u: $(systemctl is-active $u)"; done
architect_command=$(tr '\\0' ' ' < /proc/$(systemctl show -p MainPID --value gp-architect)/cmdline)
if echo "$architect_command" | grep -q -- '-observe'; then
  echo "architect: observe"
elif echo "$architect_command" | grep -q -- '-delay='; then
  echo "architect: delayed {delay}"
else
  echo "architect: acting"
fi
started=unknown
for i in $(seq 1 10); do started=$(curl -fsS -m 3 http://127.0.0.1:9101/status | jq -r .started) && break; sleep 1; done
echo "crowd started: $started"
code=000
for i in $(seq 1 15); do code=$(curl -s -m 3 -o /dev/null -w '%{{http_code}}' http://node-01/) && [ "$code" = 200 ] && break; sleep 2; done
echo "shop: $code"
""")
    report = (report + node + replica).strip()
    echo(report)
    lines = report.splitlines()
    bad = [l for l in lines if l.startswith(("shop objects:", "shop:", "gp-api:", "slots:", "products:"))
           and not l.endswith((": 0", ": 200", ": active", ": 50000"))]
    bad += [l for l in lines if l.startswith("taints: node-0") and not l.startswith("taints: node-01")
            and not l.rstrip().endswith("growing-pains/dormant")]
    bad += [l for l in lines if l.startswith("architect:") and l != f"architect: {mode}"]
    for unit in ("primary", "replica"):
        values = [l.split(":", 1)[1].strip() for l in lines if l.startswith(f"{unit} quota:")]
        if len(values) != 1:
            bad.append(f"{unit} quota missing")
            continue
        try:
            if abs(quota_seconds(values[0]) - 0.65) > 0.001:
                bad.append(f"{unit} quota: {values[0]} (want 650ms)")
        except ValueError:
            bad.append(f"{unit} quota: {values[0]} (unreadable)")
    if bad:
        raise RuntimeError("not at genesis: " + "; ".join(bad))
    echo("at genesis")
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("session")
    control = parser.add_mutually_exclusive_group()
    control.add_argument("--observe", action="store_true", help="start the architect observe-only (the control run)")
    control.add_argument("--delay", help="delay each move after its decision, for example 90s")
    args = parser.parse_args()
    try:
        reset(args.session, args.observe, delay=args.delay)
    except RuntimeError as e:
        sys.exit(str(e))
    print(f"sessions opened: {lab.connections}")


if __name__ == "__main__":
    main()
