#!/usr/bin/env python3
"""Check the completed autonomous run's report, live map, dashboard, and replica quota."""

import argparse
import json

import lab


def quota_seconds(value: str) -> float:
    if value.endswith("ms"):
        return float(value[:-2]) / 1000
    if value.endswith("s"):
        return float(value[:-1])
    raise ValueError(f"unknown quota duration: {value}")


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("session")
    args = p.parse_args()
    script = """python3 - <<'PY'
import json
import re
import time
import datetime
import urllib.parse
import urllib.error
import urllib.request

def get(url):
    return json.load(urllib.request.urlopen(url, timeout=5))

base = 'http://127.0.0.1:'
status = get(base+'9101/status')
report = None
for _ in range(18):
    try:
        report = get(base+'8080/api/report')
    except urllib.error.HTTPError as exc:
        if exc.code != 404:
            raise
    if report:
        break
    time.sleep(5)
dashboard = get(base+'3000/api/dashboards/uid/growing-pains')['dashboard']
panels = {panel['title']: panel for panel in dashboard['panels'] if 'title' in panel}
query = urllib.parse.urlencode({'query': 'gp_narrator_move_change_errors'})
moves = []
for _ in range(10):
    moves = get(base+'9090/api/v1/query?'+query)['data']['result']
    if len(moves) == 3 and all(float(row['metric']['requests']) > 0 for row in moves):
        break
    time.sleep(2)
def value(expr, at):
    query = urllib.parse.urlencode({'query': expr, 'time': at})
    return round(float(get(base+'9090/api/v1/query?'+query)['data']['result'][0]['value'][1]))
with urllib.request.urlopen(base+'8080/events', timeout=5) as stream:
    first = stream.readline().decode().removeprefix('data: ').strip()
snapshot = json.loads(first)
assert status['chapter'] == 'Finished' and status['target_rps'] == 0, status
assert report and 'Predicted path' in report['text'] and 'Observed path' in report['text'], report
totals = re.search(r'Client requests\s+(\d+); failed (\d+)', report['text'])
started = datetime.datetime.fromisoformat(status['started_at'].replace('Z', '+00:00')).timestamp()
settled_at = started + status['duration_seconds'] + 60
assert totals and (int(totals[1]), int(totals[2])) == (value('slo:run_requests:total', settled_at), value('slo:run_errors:total', settled_at)), report['text']
assert 'Worst journey' in report['text'] and 'Latency targets' in report['text'], report['text']
assert panels['Living architecture']['type'] == 'canvas'
assert len(panels['Living architecture']['targets']) == 7
assert panels['Component states']['type'] == 'state-timeline'
assert panels['Move history and change-window errors']['type'] == 'table'
assert panels['Move history and change-window errors']['targets'][0]['expr'] == 'gp_narrator_move_change_errors'
assert len(moves) == 3 and all(float(row['value'][1]) == 0 and
                               float(row['metric']['requests']) > 0 for row in moves), moves
assert snapshot['report'] and snapshot['architecture'], snapshot
assert len(snapshot['records']) == 3 and all(record['done'] for record in snapshot['records']), snapshot['records']
print(json.dumps({'status': status['chapter'], 'target_rps': status['target_rps'],
                  'report': report['text'].splitlines()[:9], 'canvas_targets': 7,
                  'move_metric_rows': len(moves), 'snapshot_records': len(snapshot['records'])}))
PY
"""
    print(lab.ssh(args.session, "ops-01", script).strip())
    quota = lab.ssh(args.session, "node-04",
                    "systemctl show -p CPUQuotaPerSecUSec --value gp-pg-replica.service").strip()
    if abs(quota_seconds(quota) - 1.5) > 0.001:
        raise RuntimeError(f"replica quota is {quota}, expected 1.5 cores")
    print(f"replica quota: {quota}")
    primary = lab.ssh(args.session, "node-01",
                      "systemctl show -p CPUQuotaPerSecUSec --value postgresql@18-main.service").strip()
    if abs(quota_seconds(primary) - 1.5) > 0.001:
        raise RuntimeError(f"primary quota is {primary}, expected 1.5 cores")
    print(f"primary quota: {primary}")


if __name__ == "__main__":
    main()
