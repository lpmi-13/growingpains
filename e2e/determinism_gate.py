#!/usr/bin/env python3
"""Repeat one seed on a private playground and judge its MVP path and SLOs.

Each run uses storyline_run.py, which resets the database and architecture to genesis and waits
for the crowd's automatic Start. The full logs stay under e2e/.logs; this command prints a
short result after each run and a final gate.

    python3 e2e/determinism_gate.py <session id> --seed 1003 --runs 10
"""

import argparse
import json
import re
import subprocess
import sys
import time
from collections import Counter
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
LOGS = ROOT / "e2e" / ".logs"
EXPECTED = ("autoscaled-fleet", "read-replica", "cache")


def seconds(clock: str) -> int:
    minute, second = map(int, clock.split(":"))
    return 60 * minute + second


def read_result(log: Path) -> dict:
    for line in reversed(log.read_text().splitlines()):
        if line.startswith("{"):
            result = json.loads(line)
            if "moves" in result and "requests" in result:
                return result
    raise ValueError(f"no complete storyline result in {log}")


def one(session: str, seed: int, number: int, tag: str = "", resume: bool = False) -> dict:
    suffix = f"-{tag}" if tag else ""
    log = LOGS / f"determinism-{seed}{suffix}-{number:02d}.log"
    result = None
    reused = False
    if resume and log.exists():
        try:
            result = read_result(log)
            reused = True
        except (ValueError, json.JSONDecodeError):
            pass  # An interrupted run has no complete result; reset and repeat it.
    if result is None:
        with log.open("w") as out:
            proc = subprocess.run([sys.executable, str(ROOT / "e2e" / "storyline_run.py"), session,
                                   "--seed", str(seed), "--unattended"],
                                  cwd=ROOT, stdout=out, stderr=subprocess.STDOUT)
        if proc.returncode:
            raise RuntimeError(f"run {number} exited {proc.returncode}; see {log}")
        result = read_result(log)
    moves = result["moves"]
    path = tuple(m["move"] for m in moves if m.get("outcome") == "completed" and m.get("verdict") == "verified")
    finished_by_15m = all(seconds(m["decided_at"]) + m.get("took_s", 9999) <= 900 for m in moves)
    move_budget = all(m.get("errors", 9999) <= 0.001 * max(m.get("requests", 0), 1) for m in moves)
    result["gate"] = (path == EXPECTED and finished_by_15m and move_budget
                      and result.get("mvp_gate", {}).get("pass") is True)
    result["path"] = path
    print(f"run {number:02}{' (resumed)' if reused else ''}: path {' → '.join(path) or 'none'}; decisions "
          f"{', '.join(m['decided_at'] for m in moves)}; "
          f"{result['errors']} errors / {result['requests']:,} requests; "
          f"SLO {result.get('mvp_gate', {}).get('pass', 'unknown')}; "
          f"{'PASS' if result['gate'] else 'FAIL'} · {log.relative_to(ROOT)}", flush=True)
    return result


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("session")
    p.add_argument("--seed", type=int, default=1003)
    p.add_argument("--runs", type=int, default=10)
    p.add_argument("--pause", type=int, default=30, help="seconds between runs for report inspection")
    p.add_argument("--tag", default="", help="short log tag, to retain earlier gate evidence")
    p.add_argument("--resume", action="store_true", help="reuse complete run logs with this tag")
    args = p.parse_args()
    if args.tag and not re.fullmatch(r"[A-Za-z0-9_-]+", args.tag):
        p.error("--tag must contain only letters, digits, underscores, or hyphens")
    LOGS.mkdir(exist_ok=True)
    results = []
    for number in range(1, args.runs + 1):
        print(f"starting run {number}/{args.runs}", flush=True)
        result = one(args.session, args.seed, number, args.tag, args.resume)
        results.append(result)
        if not result["gate"]:
            raise SystemExit(f"run {number} failed the MVP gate; stopping repeatability trials")
        if number < args.runs:
            time.sleep(args.pause)
    counts = Counter(r["path"] for r in results)
    path, same = counts.most_common(1)[0]
    decisions = [[seconds(m["decided_at"]) for m in r["moves"]] for r in results if r["path"] == path]
    spreads = [max(row[i] for row in decisions) - min(row[i] for row in decisions) for i in range(len(path))]
    passed = same >= min(9, args.runs) and path == EXPECTED and all(s <= 60 for s in spreads) and all(r["gate"] for r in results)
    print(f"\n{same}/{args.runs} same path; decision spreads {spreads} s; "
          f"{sum(r['gate'] for r in results)}/{args.runs} full MVP passes; "
          f"determinism gate {'PASS' if passed else 'FAIL'}", flush=True)
    if not passed:
        sys.exit(1)


if __name__ == "__main__":
    main()
