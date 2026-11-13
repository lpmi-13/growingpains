#!/usr/bin/env python3
"""Summarise a calibration ramp: one row per step, measured over its last 30 seconds.

    python3 tools/calibration_report.py --prometheus http://127.0.0.1:9090 --start <unix> --steps 7

Each step holds the genesis mix at one offered rate for a minute (storyline "calibrate").
"""

import argparse
import json
import urllib.parse
import urllib.request

QUERIES = {
    "offered": 'sum(rate(gp_crowd_sessions_total{event="scheduled",source="anonymous"}[30s])) + 5 * sum(rate(gp_crowd_sessions_total{event="scheduled",source="shopper"}[30s])) + 2 * sum(rate(gp_crowd_sessions_total{event="scheduled",source="seller"}[30s]))',
    "achieved": 'sum(rate(gp_crowd_requests_total{journey!~"audit|probe|setup"}[30s]))',
    "api_cpu": 'sum(rate(gp_unit_cpu_seconds_total{unit="api"}[30s])) / sum(gp_unit_cpu_limit_cores{unit="api"})',
    "api_ms_req": '1000 * sum(rate(gp_unit_cpu_seconds_total{unit="api"}[30s])) / sum(rate(gp_edge_requests_total[30s]))',
    "db_cpu": 'sum(rate(gp_unit_cpu_seconds_total{unit=~"db|db-primary"}[30s])) / sum(gp_unit_cpu_limit_cores{unit=~"db|db-primary"})',
    "db_read": 'sum(rate(gp_pg_statement_seconds_total{class="read"}[30s])) / sum(rate(gp_pg_statement_seconds_total{class=~"read|write"}[30s]))',
    "queue_p95_ms": '1000 * histogram_quantile(0.95, sum by (le) (rate(gp_queue_time_seconds_bucket[30s])))',
    "read_p95_ms": '1000 * histogram_quantile(0.95, sum by (le) (rate(gp_crowd_request_duration_seconds_bucket{kind="read",journey!~"audit|probe|setup"}[30s])))',
    "write_p95_ms": '1000 * histogram_quantile(0.95, sum by (le) (rate(gp_crowd_request_duration_seconds_bucket{kind="write"}[30s])))',
    "err_pct": '100 * (sum(rate(gp_crowd_requests_total{journey!~"audit|probe|setup",outcome=~"error_5xx|timeout|conn_error"}[30s])) or vector(0)) / sum(rate(gp_crowd_requests_total{journey!~"audit|probe|setup"}[30s]))',
    "refused": 'sum(rate(gp_crowd_sessions_total{event="refused"}[30s])) or vector(0)',
    "listing_pct": '100 * sum(rate(gp_edge_requests_total{route="listing"}[30s])) / sum(rate(gp_edge_requests_total[30s]))',
    "write_pct": '100 * sum(rate(gp_edge_requests_total{method!="GET"}[30s])) / sum(rate(gp_edge_requests_total[30s]))',
}


def query(base: str, expr: str, at: float) -> float | None:
    url = f"{base}/api/v1/query?" + urllib.parse.urlencode({"query": expr, "time": at})
    with urllib.request.urlopen(url, timeout=10) as r:
        result = json.load(r)["data"]["result"]
    if not result:
        return None
    return float(result[0]["value"][1])


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--prometheus", default="http://127.0.0.1:9090")
    p.add_argument("--start", type=float, required=True, help="unix time of Start")
    p.add_argument("--steps", type=int, required=True)
    p.add_argument("--step-seconds", type=float, default=60)
    args = p.parse_args()
    cols = list(QUERIES)
    print(" ".join(f"{c:>12}" for c in ["step", *cols]))
    for i in range(args.steps):
        at = args.start + (i + 1) * args.step_seconds - 2
        row = [query(args.prometheus, QUERIES[c], at) for c in cols]
        print(" ".join([f"{i + 1:>12}"] + [f"{v:>12.3f}" if v is not None else f"{'-':>12}" for v in row]))


if __name__ == "__main__":
    main()
