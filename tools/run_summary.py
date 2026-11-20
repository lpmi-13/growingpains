#!/usr/bin/env python3
"""Summarise a run from Prometheus: a row per minute, the first SLO breaches, and the architect's events.

    python3 tools/run_summary.py --start <unix seconds of Start> [--minutes 17]
        [--prometheus http://127.0.0.1:9090] [--architect http://127.0.0.1:7000]

This is the evidence for the build gates: "pain without a cure" (no architect, the SLOs breach)
and the MVP runs (three moves, SLOs held). e2e/ops_runner.py imports it on `ops-01`.
"""

import argparse
import datetime as dt
import http.client
import json
import math
import urllib.parse

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

# One kept-open connection per host. A summary sends ~140 queries, and a watched run polls the
# architect every few seconds; there is no reason for each to open its own.
_CONNECTIONS: dict[str, http.client.HTTPConnection] = {}


def request(method: str, url: str, timeout: float = 10):
    """Send one request on the host's kept-open connection and return the decoded JSON body.

    A GET is retried once on a fresh connection, since a server may close an idle one; a POST is not.
    """
    u = urllib.parse.urlsplit(url)
    path = u.path + (f"?{u.query}" if u.query else "")
    for attempt in (0, 1):
        conn = _CONNECTIONS.get(u.netloc)
        if conn is None:
            conn = _CONNECTIONS[u.netloc] = http.client.HTTPConnection(u.netloc, timeout=timeout)
        try:
            conn.request(method, path)
            r = conn.getresponse()
            body = r.read()
        except (http.client.HTTPException, OSError):
            conn.close()
            del _CONNECTIONS[u.netloc]
            if attempt or method != "GET":
                raise
            continue
        if r.status >= 400:
            raise OSError(f"{method} {url}: HTTP {r.status}: {body[:200]!r}")
        # An empty event log encodes as null.
        return json.loads(body) if body.strip() else None


def query(base: str, expr: str, at: float) -> float:
    result = request("GET", f"{base}/api/v1/query?" + urllib.parse.urlencode({"query": expr, "time": at}))["data"]["result"]
    return float(result[0]["value"][1]) if result else math.nan


def row(base: str, start: float, minute: int) -> dict:
    return {k: query(base, e, start + 60 * minute) for k, e in ROWS.items()}


def header() -> str:
    return f"{'t':>5} " + " ".join(f"{k:>9}" for k in ROWS)


def line(minute: int, values: dict) -> str:
    return f"{minute:2d}:00 " + " ".join(f"{v:9.3f}" if abs(v) < 10 else f"{v:9.0f}" for v in values.values())


def summarize(start: float, minutes: int, prometheus: str, architect: str, out=print) -> None:
    out(header())
    first = {}
    for minute in range(1, minutes + 1):
        values = row(prometheus, start, minute)
        for name, bad in [("read p95 over 250 ms", values["p95_ms"] > 250),
                          ("1-minute availability under 99.5%", values["avail_1m"] < 0.995)]:
            if bad and name not in first:
                first[name] = minute
        out(line(minute, values))
    out("\nfirst breach: " + str({k: f"t+{v}:00" for k, v in first.items()} or "none"))
    try:
        events = request("GET", architect + "/api/events", timeout=5) or []
    except OSError:
        events = []
    if events:
        out("\narchitect events:")
    for e in events:
        at = dt.datetime.fromisoformat(e["at"][:26].rstrip("Z") + "+00:00").timestamp() - start
        t = f"t+{int(at) // 60}:{int(at) % 60:02d}" if at >= 0 else "genesis"
        out(f"  {t:>8} {e['kind']:16} {e.get('pressure', ''):15} {(e.get('text') or '')[:110]}")


def mvp_gate(start: float, minutes: int, prometheus: str) -> dict:
    """Judge the full run from client-side recording rules, including every minute's latency."""
    latency = [query(prometheus, "slo:latency_ok:1m", start + 60 * m) for m in range(1, minutes + 1)]
    good = sum(math.isfinite(v) and v >= 1 for v in latency)
    at = start + 60 * minutes + 15
    # A five-minute SLI is only an in-run window once five minutes of run traffic exist.
    # The earlier samples include genesis and the reset between repeat runs.
    availability = query(prometheus, f"min_over_time(slo:availability:worst_rate5m[{max(minutes-5, 1)}m])", at)
    ryw = query(prometheus, "slo:read_your_writes_violations:total", at)
    lost = query(prometheus, "slo:acknowledged_writes_lost:total", at)
    checks = {
        "availability": math.isfinite(availability) and availability >= 0.995,
        "latency": good / minutes >= 0.95,
        "read_your_writes": ryw == 0,
        "writes_lost": lost == 0,
    }
    return {"pass": all(checks.values()), "checks": checks, "worst_5m_availability": availability,
            "latency_windows_ok": good, "latency_windows_total": minutes,
            "read_your_writes_violations": ryw, "writes_lost": lost}


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--start", type=float, required=True)
    p.add_argument("--minutes", type=int, default=17)
    p.add_argument("--prometheus", default="http://127.0.0.1:9090")
    p.add_argument("--architect", default="http://127.0.0.1:7000")
    args = p.parse_args()
    summarize(args.start, args.minutes, args.prometheus, args.architect)


if __name__ == "__main__":
    main()
