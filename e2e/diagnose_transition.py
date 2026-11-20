#!/usr/bin/env python3
"""Inspect per-minute database work around a completed run's replica-to-cache transition."""

import argparse

import lab


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("session")
    parser.add_argument("--start", type=int, required=True, help="Unix time when the crowd started")
    args = parser.parse_args()
    script = f"""cd /var/tmp/gp-harness
python3 - <<'PY'
import ops_runner
ops_runner.WINDOWS = [(8, 9), (9, 10), (10, 11), (11, 12)]
ops_runner.print_windows(ops_runner.statement_windows({args.start}))
PY
"""
    print(lab.ssh(args.session, "ops-01", script).strip())


if __name__ == "__main__":
    main()
