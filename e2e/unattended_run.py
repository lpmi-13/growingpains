#!/usr/bin/env python3
"""Reset a private session, let the crowd start itself, and verify the complete MVP run."""

import argparse
import json

import lab
import reset_genesis

EXPECTED = ("autoscaled-fleet", "read-replica", "cache")


def seconds(clock: str) -> int:
    minutes, seconds_part = map(int, clock.split(":"))
    return 60 * minutes + seconds_part


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("session")
    parser.add_argument("--seed", type=int, default=1003)
    args = parser.parse_args()

    say = lambda line: print(line, flush=True)
    reset_genesis.reset(args.session, echo=say)
    result = lab.run_on_ops(args.session, "--seed", str(args.seed), "--until", "storyline", "--unattended", echo=say)
    moves = result.get("moves", [])
    path = tuple(m.get("move") for m in moves if m.get("outcome") == "completed" and m.get("verdict") == "verified")
    passed = (path == EXPECTED and len(moves) == 3
              and result.get("mvp_gate", {}).get("pass") is True
              and result.get("errors", float("inf")) <= 0.005 * max(result.get("requests", 0), 1)
              and all(seconds(m["decided_at"]) + m["took_s"] <= 15 * 60 for m in moves)
              and all(m["errors"] <= 0.001 * max(m["requests"], 1) for m in moves))
    say(json.dumps({"auto_started": True, "seed": args.seed, "path": path,
                    "requests": result.get("requests"), "errors": result.get("errors"),
                    "mvp_gate": result.get("mvp_gate"), "pass": passed}))
    if not passed:
        raise SystemExit("unattended MVP run failed")


if __name__ == "__main__":
    main()
