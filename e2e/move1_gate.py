#!/usr/bin/env python3
"""Stage 3's gate on a private session: the fleet migration (Move 1), error-free, run after run.

    python3 e2e/move1_gate.py <session id> --seeds 1001 1002 1003

For each seed: shrink the session to genesis (e2e/reset_genesis.py), let the shop run a calm
minute, press Start, wait for the architect to finish the fleet move, and count every crowd
request from the decision to the verdict. A run passes when the move completed with no 5xx,
timeout, or connection error. One JSON line per run goes to stdout, and the run's progress to
stderr; the run then stops early, since the gate is about Move 1 only.

The waiting happens on `ops-01` (e2e/ops_runner.py), so a run costs five sessions through the
platform: four for the reset and one to follow the run.
"""

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import lab  # noqa: E402
import reset_genesis  # noqa: E402


def progress(text):
    print(text, file=sys.stderr, flush=True)


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("session")
    p.add_argument("--seeds", type=int, nargs="+", required=True)
    args = p.parse_args()
    passed = verified = 0
    for seed in args.seeds:
        try:
            reset_genesis.reset(args.session, echo=progress)
            r = lab.run_on_ops(args.session, "--seed", str(seed), "--until", "fleet", echo=progress)
        except Exception as e:  # keep going: one broken run is a finding, not the end of the gate
            r = {"seed": seed, "pass": False, "outcome": f"error: {str(e)[-300:]}"}
        passed += r["pass"]
        verified += r["pass"] and r.get("verified", False)
        print(json.dumps(r), flush=True)
    lab.ssh(args.session, "ops-01", "systemctl stop gp-crowd")
    print(json.dumps({"passed": passed, "verified": verified, "runs": len(args.seeds),
                      "sessions_opened": lab.connections}), flush=True)


if __name__ == "__main__":
    main()
