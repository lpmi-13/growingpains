#!/usr/bin/env python3
"""Inspect a completed autonomous run on a private session before resetting it.

    python3 e2e/inspect_run.py <session id>
"""

import argparse
import base64
import json
from pathlib import Path

import lab

ROOT = Path(__file__).resolve().parents[1]


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("session")
    args = p.parse_args()
    source = base64.b64encode((ROOT / "tools" / "run_summary.py").read_bytes()).decode()
    script = f"""base64 -d > /var/tmp/gp-run-summary.py <<'GP_SUMMARY'
{source}
GP_SUMMARY
python3 - <<'PY'
import datetime
import importlib.util
import json
import urllib.request

spec = importlib.util.spec_from_file_location("summary", "/var/tmp/gp-run-summary.py")
rs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rs)
status = json.load(urllib.request.urlopen("http://127.0.0.1:9101/status"))
state = json.load(urllib.request.urlopen("http://127.0.0.1:7000/api/state"))["state"]
started = datetime.datetime.fromisoformat(status["started_at"].replace("Z", "+00:00")).timestamp()
moves = [{{"move": r["move"], "at": round((datetime.datetime.fromisoformat(r["started"].replace("Z", "+00:00")).timestamp() - started)),
          "took_s": round((datetime.datetime.fromisoformat(r["ended"].replace("Z", "+00:00")).timestamp() -
                           datetime.datetime.fromisoformat(r["started"].replace("Z", "+00:00")).timestamp())),
          "verdict": r["verdict"]}} for r in state["history"]]
print(json.dumps({{"seed": status["seed"], "moves": moves,
                  "mvp_gate": rs.mvp_gate(started, 17, "http://127.0.0.1:9090")}}))
PY
"""
    print(lab.ssh(args.session, "ops-01", script).strip())


if __name__ == "__main__":
    main()
