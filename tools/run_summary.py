#!/usr/bin/env python3
"""Summarise a run from Prometheus: a row per minute, the first SLO breaches, and the architect's events.

    python3 tools/run_summary.py --start <unix seconds of Start> [--minutes 17]
        [--prometheus http://127.0.0.1:9090] [--architect http://127.0.0.1:7000]

This is the evidence for the build gates: "pain without a cure" (no architect, the SLOs breach)
and the MVP runs (three moves, SLOs held).
"""

import argparse
import datetime as dt
import json
import math
import urllib.parse
import urllib.request

ROWS = {
    "offered": "sum(crowd_storyline_target_rps)",
    "achieved": "crowd:achieved_requests:rate1m",
    "api": 'sum(rate(process_cpu_seconds_total{job="api"}[30s])) / sum(gp_cpu_limit_cores{job="api"})',
    "db": 'max(sum by (unit) (rate(gp_unit_cpu_seconds_total{unit=~"db-.*"}[30s])) / sum by (unit) (gp_unit_cpu_limit_cores{unit=~"db-.*"}))',
    "p95_ms": "1000 * slo:read_latency_seconds:p95_1m",
    "avail_1m": "min(slo:availability:ratio_rate1m)",
    "errors_s": "sum(slo:errors:rate1m) or vector(0)",
    "repeat": 'max(gp_edge_shape{signal="repeat_ratio_cacheable"})',
}


def query(base: str, expr: str, at: float) -> float:
    url = f"{base}/api/v1/query?" + urllib.parse.urlencode({"query": expr, "time": at})
    with urllib.request.urlopen(url, timeout=10) as r:
        result = json.load(r)["data"]["result"]
    return float(result[0]["value"][1]) if result else math.nan


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--start", type=float, required=True)
    p.add_argument("--minutes", type=int, default=17)
    p.add_argument("--prometheus", default="http://127.0.0.1:9090")
    p.add_argument("--architect", default="http://127.0.0.1:7000")
    args = p.parse_args()
    print(f"{'t':>5} " + " ".join(f"{k:>9}" for k in ROWS))
    first = {}
    for minute in range(1, args.minutes + 1):
        at = args.start + 60 * minute
        row = {k: query(args.prometheus, e, at) for k, e in ROWS.items()}
        for name, bad in [("read p95 over 250 ms", row["p95_ms"] > 250), ("1-minute availability under 99.5%", row["avail_1m"] < 0.995)]:
            if bad and name not in first:
                first[name] = minute
        print(f"{minute:2d}:00 " + " ".join(f"{v:9.3f}" if abs(v) < 10 else f"{v:9.0f}" for v in row.values()))
    print("\nfirst breach:", {k: f"t+{v}:00" for k, v in first.items()} or "none")
    try:
        with urllib.request.urlopen(args.architect + "/api/events", timeout=5) as r:
            events = json.load(r) or []
    except OSError:
        events = []
    if events:
        print("\narchitect events:")
    for e in events:
        at = dt.datetime.fromisoformat(e["at"][:26].rstrip("Z") + "+00:00").timestamp() - args.start
        t = f"t+{int(at) // 60}:{int(at) % 60:02d}" if at >= 0 else "genesis"
        print(f"  {t:>8} {e['kind']:16} {e.get('pressure', ''):15} {(e.get('text') or '')[:110]}")


if __name__ == "__main__":
    main()
