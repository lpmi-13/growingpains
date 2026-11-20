#!/usr/bin/env python3
"""Locate a live run's worst availability series and missed latency minutes."""

import argparse
import json

import lab


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("session")
    p.add_argument("--start", type=int, required=True, help="Unix time when the crowd started")
    args = p.parse_args()
    script = f"""python3 - <<'PY'
import datetime
import json
import urllib.parse
import urllib.request

start = {args.start}
base = 'http://127.0.0.1:9090/api/v1/'
def query_range(expr, step=5):
    params = urllib.parse.urlencode(dict(query=expr, start=start, end=start+17*60+15, step=step))
    return json.load(urllib.request.urlopen(base+'query_range?'+params))['data']['result']

worst = []
full = []
for series in query_range('slo:availability:ratio_rate5m'):
    if series['values']:
        t, value = min(((int(t), float(v)) for t, v in series['values']), key=lambda item: item[1])
        worst.append((value, t-start, series['metric'].get('journey')))
        window = [(int(t), float(v)) for t, v in series['values'] if t >= start+5*60+15]
        if window:
            t, value = min(window, key=lambda item: item[1])
            full.append((value, t-start, series['metric'].get('journey')))
print('worst journey availability:', sorted(worst)[:12])
print('full in-run windows:', sorted(full)[:12])
for minute in range(1, 18):
    at = start+60*minute
    q = urllib.parse.urlencode(dict(query='slo:latency_ok:1m', time=at))
    res = json.load(urllib.request.urlopen(base+'query?'+q))['data']['result']
    value = float(res[0]['value'][1]) if res else None
    if value != 1:
        print('latency minute:', minute, value)
for series in query_range('sum by (journey,outcome) (increase(gp_crowd_requests_total{{phase="run",outcome=~"timeout|error_5xx|conn_error"}}[1m]))', 60):
    nonzero = [(int(t)-start, round(float(v),2)) for t,v in series['values'] if float(v)>0.01]
    if nonzero:
        print('errors:', series['metric'], nonzero)
PY
"""
    print(lab.ssh(args.session, "ops-01", script).strip())


if __name__ == "__main__":
    main()
