"""The shop's own telemetry. The architect reads only this, the agents, and the judge's SLO burn.

Shape signals that need a whole-fleet view (key repetition, anonymous share, the busiest
client, writes per seller, and search behaviour) are computed at the edge, where nginx sees
every request, from the response headers this API sets. See cmd/gp-agent.
"""

import os
import time

from prometheus_client import Counter, Gauge, Histogram

from . import settings

LATENCY_BUCKETS = (0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5)
QUEUE_BUCKETS = (0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5)

IMPL = settings.IMPL

REQUESTS = Counter("gp_http_requests_total", "Requests served", ["route", "method", "status", "impl"])
DURATION = Histogram("gp_http_request_duration_seconds", "Time in the handler", ["route", "impl"], buckets=LATENCY_BUCKETS)
QUEUE_TIME = Histogram(
    "gp_queue_time_seconds", "Time between nginx accepting a request and the handler starting",
    ["impl"], buckets=QUEUE_BUCKETS,
)
REQUEST_CPU = Counter("gp_request_cpu_seconds_total", "CPU spent in handlers, by route", ["route", "impl"])
SPAN_SECONDS = Counter("gp_span_seconds_total", "Wall time per span class", ["route", "span", "impl"])
SPAN_CPU = Counter("gp_span_cpu_seconds_total", "CPU per span class, where it can be measured", ["span", "impl"])
STATEMENTS = Counter("gp_db_statements_total", "SQL statements run, by route and database role", ["route", "role", "impl"])
ROWS = Counter("gp_db_rows_total", "Rows returned or affected, by route", ["route", "impl"])
READ_ROUTING = Counter("gp_read_routing_total", "Where eligible reads went, and why", ["role", "reason", "impl"])
REPLICA_LAG = Gauge("gp_replica_lag_seconds", "Replica lag as this instance last measured it", ["replica"])
CACHE = Counter("gp_cache_requests_total", "Cache lookups", ["route", "result", "impl"])
CONFIG_VERSION = Gauge("gp_config_version", "The runtime config version this instance is running")
CONFIG_ERRORS = Counter("gp_config_poll_errors_total", "Failed runtime config polls", ["error"])
INFLIGHT = Gauge("gp_inflight_requests", "Requests in the handler now")
IDEMPOTENT_REPLAYS = Counter("gp_idempotent_replays_total", "Writes answered from a stored idempotency key", ["route"])
START_TIME = Gauge("gp_instance_start_time_seconds", "When this instance started")
START_TIME.set(time.time())


def _cgroup_dir() -> str | None:
    try:
        with open("/proc/self/cgroup") as f:
            for line in f:
                if line.startswith("0::"):
                    return "/sys/fs/cgroup" + line[3:].strip().rstrip("/")
    except OSError:
        return None
    return None


def cpu_limit_cores() -> float:
    """The CPU this instance may use: its cgroup quota, or the machine's CPUs if it has none."""
    path = _cgroup_dir()
    while path and path.startswith("/sys/fs/cgroup"):
        try:
            quota, period = open(os.path.join(path, "cpu.max")).read().split()
            if quota != "max":
                return int(quota) / int(period)
        except (OSError, ValueError):
            pass
        if path == "/sys/fs/cgroup":
            break
        path = os.path.dirname(path)
    return float(os.cpu_count() or 1)


CPU_LIMIT = Gauge("gp_cpu_limit_cores", "The CPU quota of this instance, in cores")
CPU_LIMIT.set_function(cpu_limit_cores)
