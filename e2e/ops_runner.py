#!/usr/bin/env python3
"""One run, watched from inside `ops-01`. The harness starts this; it is not run by hand.

    python3 ops_runner.py --seed 727274 --until storyline   # all 17 minutes
    python3 ops_runner.py --seed 1001 --until fleet         # stage 3's gate: stop after Move 1

e2e/lab.py copies this file and tools/run_summary.py to `ops-01` and starts it with
`systemd-run`, so the run does not depend on the SSH session that launched it. The harness then
follows its output over one session, instead of opening a new one through the platform for
every poll. This talks only to services on `ops-01` (the crowd, the architect, Prometheus), over
one kept-open local connection each.

It waits a calm minute (the architect's windows then hold genesis only), presses Start, and
prints the architect's events as they happen and a row a minute. The last line is
`result: {json}`: for `fleet`, the gate's verdict on Move 1; for `storyline`, the crowd's
outcomes over the whole run and for each move from decision to verdict, after the summary table,
and the statement windows: database time by statement, and CPU and throttling by unit and node,
over 2:00–5:00, 5:00–8:00, 7:00–10:00, 12:00–15:00, and 15:00–17:00, from the node agents'
counters. The last two split the plateau after the cache, so a run whose replica quota is lifted
at 15:00 (e2e/storyline_run.py --lift-replica-at) has one window capped and one not.
With --unattended, it waits for the crowd's automatic Start and never calls the Start endpoint.
"""

import argparse
import json
import re
import time
import urllib.parse
from datetime import datetime

import run_summary as rs

CROWD = "http://127.0.0.1:9101"
ARCHITECT = "http://127.0.0.1:7000"
PROMETHEUS = "http://127.0.0.1:9090"
STORYLINE_S = 17 * 60
FLEET_WAIT_S = 9 * 60
POLL_S = 5


def ts(at: str) -> float:
    # RFC 3339 with nanoseconds; Python's parser takes at most microseconds.
    at = re.sub(r"\.(\d+)", lambda m: "." + m.group(1)[:6].ljust(6, "0"), at.replace("Z", "+00:00"))
    return datetime.fromisoformat(at).timestamp()


def clock(seconds: float) -> str:
    s = int(round(seconds))
    return f"{s // 60}:{s % 60:02d}" if s >= 0 else "genesis"


def events() -> list:
    return rs.request("GET", f"{ARCHITECT}/api/events?after=0", timeout=5) or []


def outcomes(start: float, end: float) -> dict:
    window = max(1, int(end - start))
    q = f'sum by (outcome) (increase(gp_crowd_requests_total{{journey!~"audit|setup"}}[{window}s]))'
    url = f"{PROMETHEUS}/api/v1/query?" + urllib.parse.urlencode({"query": q, "time": f"{end:.0f}"})
    return {r["metric"]["outcome"]: round(float(r["value"][1])) for r in rs.request("GET", url)["data"]["result"]}


def run_outcomes() -> dict:
    """The current run's settled counters, including its first scrape and drained sessions."""
    q = 'sum by (outcome) (gp_crowd_requests_total{phase="run",journey!~"audit|probe|setup"})'
    url = f"{PROMETHEUS}/api/v1/query?" + urllib.parse.urlencode({"query": q})
    return {r["metric"]["outcome"]: round(float(r["value"][1])) for r in rs.request("GET", url)["data"]["result"]}


def errors(counts: dict) -> int:
    return sum(v for k, v in counts.items() if k != "ok")


def finished(evs: list, decision: dict) -> dict | None:
    return next((e for e in evs if e["kind"] in ("completed", "rolled_back") and e.get("move") == decision.get("move")
                 and ts(e["at"]) >= ts(decision["at"])), None)


def fleet_verdict(seed: int, started: float, evs: list) -> dict | None:
    decision = next((e for e in evs if e["kind"] == "decision" and e.get("move") == "autoscaled-fleet"), None)
    end = decision and finished(evs, decision)
    if not end:
        return None
    counts = outcomes(ts(decision["at"]), ts(end["at"]))
    return {"seed": seed, "pass": end["kind"] == "completed" and errors(counts) == 0,
            "decided_at_s": round(ts(decision["at"]) - started), "took_s": round(ts(end["at"]) - ts(decision["at"])),
            "outcome": end["kind"], "verdict": end.get("text", ""), "verified": end.get("text") == "verified",
            "requests": sum(counts.values()), "errors": errors(counts)}


# Statement windows, in storyline minutes: chapter 1's excess over the twin (2:00–5:00), then the
# windows the twin's database costs were fitted on (docs/calibration.md), with the plateau after
# the cache split at 15:00, where a measurement run lifts the replica's quota.
WINDOWS = ((2, 5), (5, 8), (7, 10), (12, 15), (15, 17))


def by_labels(expr: str, at: float, *labels: str) -> dict:
    url = f"{PROMETHEUS}/api/v1/query?" + urllib.parse.urlencode({"query": expr, "time": f"{at:.0f}"})
    return {tuple(r["metric"].get(k, "") for k in labels): float(r["value"][1])
            for r in rs.request("GET", url)["data"]["result"]}


def statement_windows(started: float) -> list:
    """Each window's database time by statement, CPU and throttling by unit, and CPU by node: the twin's cost evidence."""
    out = []
    for a, b in WINDOWS:
        end, w = started + 60 * b, 60 * (b - a)
        secs = by_labels(f"sum by (node, stmt) (increase(gp_pg_statement_seconds_total[{w}s]))", end, "node", "stmt")
        calls = by_labels(f"sum by (node, stmt) (increase(gp_pg_statement_calls_total[{w}s]))", end, "node", "stmt")
        units = by_labels(f"sum by (node, unit) (increase(gp_unit_cpu_seconds_total[{w}s]))", end, "node", "unit")
        throttled = by_labels(f"sum by (node, unit) (increase(gp_unit_throttled_seconds_total[{w}s]))", end,
                              "node", "unit")
        busy = by_labels(f'sum by (node) (increase(node_cpu_seconds_total{{mode!~"idle|iowait"}}[{w}s]))', end, "node")
        stmts = [{"node": n, "stmt": s, "calls": round(calls.get((n, s), 0)), "seconds": round(v, 3)}
                 for (n, s), v in secs.items() if v > 0]
        out.append({"window": f"{a}:00-{b}:00", "seconds": w, "requests": sum(outcomes(end - w, end).values()),
                    "statements": sorted(stmts, key=lambda r: -r["seconds"]),
                    "unit_cores": {f"{n}/{u}": round(v / w, 3) for (n, u), v in sorted(units.items())},
                    # The share of the window each unit spent throttled by its quota.
                    "unit_throttled": {f"{n}/{u}": round(v / w, 3) for (n, u), v in sorted(throttled.items())},
                    "node_busy_cores": {n: round(v / w, 3) for (n,), v in sorted(busy.items())}})
    return out


def print_windows(windows: list) -> None:
    for w in windows:
        s = w["seconds"]
        db = {k: v for k, v in w["unit_cores"].items() if "/db-" in k}
        thr = w.get("unit_throttled", {})
        stmt_s = sum(r["seconds"] for r in w["statements"])
        print(f"\nstatements {w['window']}: {w['requests'] / s:.0f} req/s; statement time {stmt_s / s:.3f} s/s; "
              f"database CPU {', '.join(f'{k} {v:.3f} (throttled {thr.get(k, 0):.2f})' for k, v in db.items()) or 'none'}; "
              f"busy {', '.join(f'{k} {v:.2f}' for k, v in w['node_busy_cores'].items())}", flush=True)
        for r in w["statements"][:14]:
            print(f"  {r['node']:8} {r['stmt']:24} {r['calls'] / s:8.1f} calls/s {1000 * r['seconds'] / max(r['calls'], 1):8.3f}"
                  f" ms/call {r['seconds'] / s:7.3f} s/s", flush=True)


def storyline_result(seed: int, started: float, evs: list) -> dict:
    whole = run_outcomes()
    result = {"seed": seed, "started": round(started), "requests": sum(whole.values()), "errors": errors(whole),
              "outcomes": whole, "moves": []}
    for d in (e for e in evs if e["kind"] == "decision"):
        row = {"move": d.get("move"), "decided_at": clock(ts(d["at"]) - started)}
        if end := finished(evs, d):
            counts = outcomes(ts(d["at"]), ts(end["at"]))
            row.update(took_s=round(ts(end["at"]) - ts(d["at"])), outcome=end["kind"], verdict=end.get("text", ""),
                       requests=sum(counts.values()), errors=errors(counts))
        result["moves"].append(row)
    return result


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--seed", type=int, required=True)
    p.add_argument("--until", choices=("fleet", "storyline"), required=True)
    p.add_argument("--calm", type=int, default=60, help="seconds of genesis before Start")
    p.add_argument("--unattended", action="store_true", help="observe the crowd's automatic Start; do not press Start")
    args = p.parse_args()

    if args.unattended:
        started = None
        for elapsed in range(0, 7 * 60, POLL_S):
            status = rs.request("GET", f"{CROWD}/status", timeout=5)
            if status.get("started"):
                if status["seed"] != args.seed:
                    raise RuntimeError(f"automatic Start used seed {status['seed']}, expected {args.seed}")
                started = ts(status["started_at"])
                break
            if elapsed % 30 == 0:
                print(f"waiting for automatic Start: {elapsed}s", flush=True)
            time.sleep(POLL_S)
        if started is None:
            raise RuntimeError("the crowd did not start itself within seven minutes")
        print(f"seed {args.seed} auto-started at {started:.0f}", flush=True)
    else:
        time.sleep(args.calm)
        started = ts(rs.request("POST", f"{CROWD}/start?seed={args.seed}")["started_at"])
        print(f"seed {args.seed} started at {started:.0f}", flush=True)
    deadline = started + (STORYLINE_S + 30 if args.until == "storyline" else FLEET_WAIT_S)
    seen, minute, evs = 0, 1, []
    print("  " + rs.header(), flush=True)
    while time.time() < deadline:
        time.sleep(POLL_S)
        try:
            evs = events()
        except OSError as e:
            print(f"  events unavailable: {str(e)[:120]}", flush=True)
            continue
        for e in evs[seen:]:
            print(f"  {clock(ts(e['at']) - started):>7} {e['kind']:14} {e.get('move') or e.get('pressure') or '':16} "
                  f"{(e.get('text') or '')[:100]}", flush=True)
        seen = len(evs)
        # A row a minute, once the recording rules have caught up. It is also the follower's heartbeat.
        while time.time() >= started + 60 * minute + 15:
            try:
                print("  " + rs.line(minute, rs.row(PROMETHEUS, started, minute)), flush=True)
            except OSError as e:
                print(f"  {minute:2d}:00 unavailable: {str(e)[:120]}", flush=True)
            minute += 1
        if args.until == "fleet" and (verdict := fleet_verdict(args.seed, started, evs)):
            print("result: " + json.dumps(verdict), flush=True)
            return

    if args.until == "fleet":
        print("result: " + json.dumps({"seed": args.seed, "pass": False, "outcome": "no fleet move within 9 minutes"}),
              flush=True)
        return
    print(flush=True)
    rs.summarize(started, 17, PROMETHEUS, ARCHITECT, out=lambda s: print(s, flush=True))
    result = storyline_result(args.seed, started, events())
    try:
        result["mvp_gate"] = rs.mvp_gate(started, 17, PROMETHEUS)
        print("\nMVP SLO gate: " + json.dumps(result["mvp_gate"]), flush=True)
    except (OSError, KeyError, ValueError) as e:
        print(f"MVP SLO gate unavailable: {str(e)[:200]}", flush=True)
    try:
        result["windows"] = statement_windows(started)
        print_windows(result["windows"])
    except (OSError, KeyError, ValueError) as e:
        print(f"statement windows unavailable: {str(e)[:200]}", flush=True)
    print("result: " + json.dumps(result), flush=True)


if __name__ == "__main__":
    main()
