#!/usr/bin/env python3
"""A full storyline on a private session: every move, or none for the control run.

    python3 e2e/storyline_run.py <session id> --seed 727274
    python3 e2e/storyline_run.py <session id> --seed 727274 --observe    # stage 2 on VMs
    python3 e2e/storyline_run.py <session id> --seed 1003 --lift-replica-at 15:00

For the seed: shrink the session to genesis (e2e/reset_genesis.py, with the architect acting,
or observe-only with `--observe`), let the shop run a calm minute and press Start. With
`--unattended`, wait for the crowd's own three-minute Start instead. Follow the storyline's
17 minutes: the architect's events as they happen and a row a minute. Then print
tools/run_summary.py's table, the statement windows (database time by statement, CPU by unit),
the crowd's outcomes for the whole run and for each move from decision to verdict, and a JSON
line with the same numbers.

`--lift-replica-at 15:00` removes the read replica's CPU quota at that storyline time, for the
rest of the run. Under the quota the replica is throttled, and its statement time measures
waiting as well as CPU; uncapped, the 15:00-17:00 window gives each statement's real CPU on the
plateau after the cache. The architect sees the replica's limit rise to the machine's CPUs, so
what it decides after the lift doesn't count. The lift is runtime only, and the next reset
removes it.

The run is watched from inside `ops-01` (e2e/ops_runner.py), so it costs five sessions through
the platform: four for the reset and one to follow the run. A lift costs one more.
"""

import argparse
import json
import re
import subprocess
import sys
import threading
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import lab  # noqa: E402
import reset_genesis  # noqa: E402

REPLICA_VM = "node-04"
# The session to node-04 opens this long before the lift, and the script there waits for the
# moment on node-04's own clock, which chrony keeps within microseconds of ops-01's, where the
# run's start was stamped.
LIFT_LEAD_S = 45
LIFT = """
target={target}
systemctl is-active -q gp-pg-replica.service || {{ echo "no replica is running on node-04" >&2; exit 1; }}
delay=$(( target - $(date +%s) ))
if [ "$delay" -gt 0 ]; then sleep "$delay"; fi
systemctl set-property --runtime gp-pg-replica.service CPUQuota=
echo "lifted at $(date +%s): CPUQuotaPerSecUSec=$(systemctl show -p CPUQuotaPerSecUSec --value gp-pg-replica.service)," \
  "cpu.max $(cat /sys/fs/cgroup/system.slice/gp-pg-replica.service/cpu.max)"
"""


def storyline_seconds(at: str) -> int:
    m = re.fullmatch(r"(\d{1,2}):([0-5]\d)", at)
    if not m or not 0 < 60 * int(m[1]) + int(m[2]) < 17 * 60:
        raise argparse.ArgumentTypeError(f"{at!r}: give a storyline time between 0:01 and 16:59, like 15:00")
    return 60 * int(m[1]) + int(m[2])


def clock(seconds: int) -> str:
    return f"{seconds // 60}:{seconds % 60:02d}"


def lift_replica(play: str, target: int, out: dict, echo) -> None:
    """At `target` (epoch seconds), remove the replica's CPU quota on node-04, in one session."""
    time.sleep(max(0.0, target - LIFT_LEAD_S - time.time()))
    try:
        out["report"] = lab.ssh(play, REPLICA_VM, LIFT.format(target=target), timeout=LIFT_LEAD_S + 120).strip()
    except (RuntimeError, subprocess.TimeoutExpired) as e:
        out["error"] = str(e).strip()[-300:]
    echo(f"  replica quota: {out.get('report') or 'not lifted: ' + out.get('error', '')}")


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("session")
    p.add_argument("--seed", type=int, required=True)
    p.add_argument("--observe", action="store_true", help="the control run: the architect never moves")
    p.add_argument("--delay", help="timing control: delay every move, for example 90s")
    p.add_argument("--calm", type=int, default=60,
                   help="seconds of genesis before Start (default 60)")
    p.add_argument("--unattended", action="store_true",
                   help="wait for the crowd's automatic Start instead of pressing Start")
    p.add_argument("--lift-replica-at", metavar="M:SS", type=storyline_seconds,
                   help="remove the read replica's CPU quota at this storyline time, for example 15:00")
    args = p.parse_args()
    if args.observe and args.delay:
        p.error("--observe and --delay are mutually exclusive")
    if args.calm < 0:
        p.error("--calm must be nonnegative")

    say = lambda s: print(s, flush=True)  # noqa: E731
    lift: dict = {}

    def watch(line: str) -> None:
        say(line)
        # The runner's first line stamps the start on ops-01's clock.
        if args.lift_replica_at and not lift and (m := re.match(r"seed \d+ started at (\d+)", line)):
            lift["target"] = int(m[1]) + args.lift_replica_at
            lift["thread"] = threading.Thread(target=lift_replica, daemon=True,
                                              args=(args.session, lift["target"], lift, say))
            lift["thread"].start()

    reset_genesis.reset(args.session, args.observe, echo=say, delay=args.delay)
    runner_args = ["--seed", str(args.seed), "--until", "storyline"]
    if args.unattended:
        runner_args.append("--unattended")
    else:
        runner_args.extend(("--calm", str(args.calm)))
    result = lab.run_on_ops(args.session, *runner_args, echo=watch)
    result["observe"] = args.observe
    if args.delay:
        result["delay"] = args.delay
    if args.lift_replica_at:
        if "thread" in lift:
            lift.pop("thread").join(timeout=LIFT_LEAD_S + 130)
        result["replica_lift"] = {"at": clock(args.lift_replica_at), **lift}

    say(f"\nrun: {result['requests']:,} requests, {result['errors']:,} errors "
        f"({100 * (1 - result['errors'] / max(result['requests'], 1)):.3f}% ok)")
    for m in result["moves"]:
        say(f"  {m['move']:18} decided {m['decided_at']:>6}  " +
            (f"took {m['took_s']} s, {m['outcome']} ({m['verdict']}), {m['errors']} errors in {m['requests']:,}"
             if "took_s" in m else "not finished"))
    result["sessions_opened"] = lab.connections
    say(f"sessions opened: {lab.connections}")
    say(json.dumps(result))


if __name__ == "__main__":
    main()
