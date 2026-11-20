"""The e2e harness's only way into a private session, with every connection counted.

Each `labctl ssh` opens a new session through the platform, so the harness keeps them few:

- a reset (e2e/reset_genesis.py) uses one per machine it changes, four in all;
- a run is watched from inside `ops-01` by e2e/ops_runner.py, started detached, and followed here
  over one session. It is not polled from here.

Break Team's harness once opened a connection per request through the platform (~230 a
scenario); see its docs/spike-results.md, "Connections through the port".
"""

import json
import os
import queue
import signal
import subprocess
import tempfile
import threading
import time
from collections import deque
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
REMOTE = "/var/tmp/gp-harness"
UNIT = "gp-harness-run"  # the runner's transient unit on ops-01; a reset stops it
# The runner prints at least a row a minute. A follow that hears nothing for this long is assumed
# to be stuck in the tunnel, and is replaced.
SILENCE_S = 150
ENDED = "gp-harness: runner ended"

connections = 0


def _labctl_ssh(play: str, vm: str) -> list[str]:
    global connections
    connections += 1
    return ["labctl", "ssh", play, "--machine", vm, "--user", "root", "--", "bash", "-s"]


def ssh(play: str, vm: str, script: str, timeout: int = 600) -> str:
    """Run a bash script as root on one VM, in one session. Exits on failure with its output."""
    out = subprocess.run(_labctl_ssh(play, vm), input="set -euo pipefail\n" + script,
                         capture_output=True, text=True, timeout=timeout)
    text = (out.stdout + out.stderr).strip()
    if out.returncode != 0:
        raise RuntimeError(f"{vm} failed ({out.returncode}):\n{text}")
    return out.stdout


def run_on_ops(play: str, *args: str, echo=print) -> dict:
    """Run e2e/ops_runner.py on ops-01 and return its result.

    One session copies the runner, starts it under systemd-run, and follows its log. Only if that
    session drops before the result does another one pick the log up where it left off.
    """
    files = "".join(f"cat > {REMOTE}/{p.name} <<'GP_HARNESS_EOF'\n{p.read_text()}GP_HARNESS_EOF\n"
                    for p in (ROOT / "e2e" / "ops_runner.py", ROOT / "tools" / "run_summary.py"))
    launch = (
        f"mkdir -p {REMOTE}\n{files}"
        f"systemctl stop {UNIT} 2>/dev/null || true\n"
        f"systemctl reset-failed {UNIT} 2>/dev/null || true\n"
        f"systemd-run --quiet --unit {UNIT} --collect -p WorkingDirectory={REMOTE} "
        f"-p StandardOutput=truncate:{REMOTE}/run.log -p StandardError=append:{REMOTE}/run.log "
        f"python3 -u {REMOTE}/ops_runner.py {' '.join(args)}\n"
    )
    seen, result, recent = 0, None, deque(maxlen=20)
    for attempt in range(5):
        # tail stops with the runner; the marker after it tells a finished runner from a dropped session.
        follow = (
            f"pid=$(systemctl show -p MainPID --value {UNIT} 2>/dev/null || true)\n"
            f'if [ -n "$pid" ] && [ "$pid" != 0 ]; then tail -n +{seen + 1} --pid="$pid" -f {REMOTE}/run.log\n'
            f"else tail -n +{seen + 1} {REMOTE}/run.log; fi\n"
            f"echo '{ENDED}'\n"
        )
        script = "set -euo pipefail\n" + (launch if attempt == 0 else "") + follow
        ended = stalled = False
        with tempfile.TemporaryFile("w+") as err:
            # Its own process group, so a stalled follow can be killed with everything it started.
            proc = subprocess.Popen(_labctl_ssh(play, "ops-01"), stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                    stderr=err, text=True, start_new_session=True)
            proc.stdin.write(script)
            proc.stdin.close()
            # Each follow has its own queue: a replaced follow's reader must not feed the next one.
            lines: queue.Queue = queue.Queue()
            threading.Thread(target=_pump, args=(proc.stdout, lines), daemon=True).start()
            while True:
                try:
                    line = lines.get(timeout=SILENCE_S)
                except queue.Empty:
                    stalled = True
                    os.killpg(proc.pid, signal.SIGKILL)
                    break
                if line is None:
                    break
                if line.rstrip("\n") == ENDED:
                    ended = True
                    continue
                seen += 1
                if line.startswith("result: "):
                    result = json.loads(line[len("result: "):])
                else:
                    recent.append(line.rstrip("\n"))
                    echo(line.rstrip("\n"))
            proc.wait()
            err.seek(0)
            stderr = err.read().strip()
        if result is not None:
            return result
        if ended:
            raise RuntimeError("the runner on ops-01 ended without a result:\n" + "\n".join(recent))
        if attempt == 0 and seen == 0 and not stalled and proc.returncode != 0:
            raise RuntimeError(f"ops-01: could not start the runner ({proc.returncode}): {stderr[-600:]}")
        why = f"no output for {SILENCE_S} s" if stalled else (stderr[-200:] or "the session closed")
        echo(f"  (lost the run's output after {seen} lines: {why}; reconnecting)")
        time.sleep(5)
    raise RuntimeError("lost the run's output five times")


def _pump(stream, lines: queue.Queue) -> None:
    for line in stream:
        lines.put(line)
    lines.put(None)
