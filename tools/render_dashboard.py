#!/usr/bin/env python3
"""Render the Growing Pains Grafana dashboard to deploy/grafana/dashboards/growingpains.json.

The rows follow the plan's "Proposed dashboard rows". Targets are drawn as thresholds, so a tile
turns red when its SLI misses. Every graph shows the architect's `move` annotations; the
narration panel lists the `narration` notes.

    python3 tools/render_dashboard.py
"""

import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / "deploy" / "grafana" / "dashboards" / "growingpains.json"
DS = {"type": "prometheus", "uid": "prometheus"}

GREEN, RED, AMBER, BLUE, GREY = "#3f9f5f", "#d9534f", "#e0a030", "#4a90d9", "#8a8a8a"

_id = 0


def next_id() -> int:
    global _id
    _id += 1
    return _id


def target(expr: str, legend: str = "", ref: str = "A", instant: bool = False) -> dict:
    t = {"datasource": DS, "expr": expr, "legendFormat": legend or "__auto", "refId": ref, "range": not instant, "instant": instant}
    return t


def thresholds(steps: list[tuple[float | None, str]]) -> dict:
    return {"mode": "absolute", "steps": [{"color": c, "value": v} for v, c in steps]}


def stat(title, exprs, x, y, w=4, h=4, unit="none", steps=None, decimals=None, text_mode="value", description="", mappings=None):
    exprs = exprs if isinstance(exprs, list) else [(exprs, "")]
    defaults = {"unit": unit, "thresholds": thresholds(steps or [(None, BLUE)]), "color": {"mode": "thresholds"}}
    if decimals is not None:
        defaults["decimals"] = decimals
    if mappings:
        defaults["mappings"] = mappings
    return {
        "id": next_id(), "type": "stat", "title": title, "description": description, "datasource": DS,
        "gridPos": {"x": x, "y": y, "w": w, "h": h},
        "targets": [target(e, l, chr(65 + i), instant=True) for i, (e, l) in enumerate(exprs)],
        "fieldConfig": {"defaults": defaults, "overrides": []},
        "options": {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                    "colorMode": "background", "graphMode": "none", "textMode": text_mode, "justifyMode": "center"},
    }


def series(title, exprs, x, y, w=12, h=8, unit="none", steps=None, stack=False, minimum=None, maximum=None, description="", fill=10):
    custom = {"lineWidth": 2, "fillOpacity": fill, "showPoints": "never", "spanNulls": True,
              "stacking": {"mode": "normal" if stack else "none", "group": "A"}}
    if steps:
        custom["thresholdsStyle"] = {"mode": "line+area" if False else "dashed"}
    defaults = {"unit": unit, "custom": custom, "color": {"mode": "palette-classic"},
                "thresholds": thresholds(steps or [(None, GREEN)])}
    if minimum is not None:
        defaults["min"] = minimum
    if maximum is not None:
        defaults["max"] = maximum
    return {
        "id": next_id(), "type": "timeseries", "title": title, "description": description, "datasource": DS,
        "gridPos": {"x": x, "y": y, "w": w, "h": h},
        "targets": [target(e, l, chr(65 + i)) for i, (e, l) in enumerate(exprs)],
        "fieldConfig": {"defaults": defaults, "overrides": []},
        "options": {"legend": {"displayMode": "list", "placement": "bottom", "showLegend": True},
                    "tooltip": {"mode": "multi", "sort": "desc"}},
    }


def row(title: str, y: int) -> dict:
    return {"id": next_id(), "type": "row", "title": title, "collapsed": False, "gridPos": {"x": 0, "y": y, "w": 24, "h": 1}, "panels": []}


def build() -> dict:
    panels = []
    y = 0
    # 1. Run bar
    panels.append(row("Run", y)); y += 1
    panels += [
        stat("Run clock", "gp_run_elapsed_seconds", 0, y, unit="s", decimals=0,
             mappings=[{"type": "range", "options": {"from": -10, "to": -0.5, "result": {"text": "Genesis: waiting for Start"}}}],
             description="Time since Start. Before Start the shop runs at genesis traffic."),
        stat("Seed", "crowd_storyline_seed", 4, y, decimals=0, description="Shown to observers. The architect never reads it."),
        stat("Architecture stage", "gp_architect_stage or vector(0)", 8, y, decimals=0,
             description="How many architecture moves have completed."),
        stat("Move in progress", 'count(gp_architect_move_active == 1) or vector(0)', 12, y, decimals=0,
             mappings=[{"type": "value", "options": {"0": {"text": "none"}}}]),
        stat("Error budget left", "slo:error_budget:remaining_ratio", 16, y, unit="percentunit", decimals=1,
             steps=[(None, RED), (0.25, AMBER), (0.5, GREEN)],
             description="0.5% of the run's requests may fail. Under 25%, only protective moves are allowed."),
        stat("Offered load", "sum(crowd_storyline_target_rps)", 20, y, unit="reqps", decimals=0,
             description="What the crowd is trying to send. Compare it with achieved requests below."),
    ]
    y += 4
    # 2. SLO tiles
    panels.append(row("SLOs", y)); y += 1
    panels += [
        stat("Availability (worst journey, 5m)", "slo:availability:worst_rate5m", 0, y, w=4, unit="percentunit", decimals=2,
             steps=[(None, RED), (0.995, GREEN)], description="Target: at least 99.5% non-5xx, non-timeout per journey."),
        stat("Read p95", "slo:read_latency_seconds:p95_5m", 4, y, w=3, unit="s", decimals=3, steps=[(None, GREEN), (0.25, RED)]),
        stat("Read p99", "slo:read_latency_seconds:p99_5m", 7, y, w=3, unit="s", decimals=3, steps=[(None, GREEN), (0.8, RED)]),
        stat("Write p95", "slo:write_latency_seconds:p95_5m", 10, y, w=3, unit="s", decimals=3, steps=[(None, GREEN), (0.4, RED)]),
        stat("Read-your-writes violations", "slo:read_your_writes_violations:total", 13, y, w=3, decimals=0, steps=[(None, GREEN), (1, RED)]),
        stat("Writes lost", "slo:acknowledged_writes_lost:total", 16, y, w=2, decimals=0, steps=[(None, GREEN), (1, RED)]),
        stat("Staleness p99 (detail)", 'slo:staleness_seconds:p99_5m{view="detail"} or vector(0)', 18, y, w=3, unit="s", decimals=2,
             steps=[(None, GREEN), (2, RED)]),
        stat("Burn rate (1m / 5m)", [("slo:burn_rate:1m", "1m"), ("slo:burn_rate:5m", "5m")], 21, y, w=3, decimals=1,
             steps=[(None, GREEN), (1, AMBER), (6, RED)], text_mode="value_and_name"),
    ]
    y += 4
    panels.append(series("Availability by journey (5m)", [("slo:availability:ratio_rate5m", "{{journey}}")], 0, y, w=12, h=7,
                         unit="percentunit", minimum=0.98, maximum=1, steps=[(None, RED), (0.995, GREEN)], fill=0))
    panels.append(series("Latency SLIs", [("slo:read_latency_seconds:p95_1m", "read p95"), ("slo:read_latency_seconds:p99_1m", "read p99"),
                                          ("slo:write_latency_seconds:p95_1m", "write p95")], 12, y, w=12, h=7, unit="s", fill=0,
                         steps=[(None, GREEN), (0.25, RED)]))
    y += 7
    # 3. Traffic
    panels.append(row("Traffic", y)); y += 1
    panels.append(series("Requests per second by operation", [("sum by (route) (rate(gp_edge_requests_total[30s]))", "{{route}}")],
                         0, y, w=12, unit="reqps", stack=True, fill=40))
    panels.append(series("Offered and achieved", [
        ("sum(crowd_storyline_target_rps)", "offered (storyline target)"),
        ("crowd:achieved_requests:rate1m", "achieved"),
        ('sum(rate(gp_crowd_requests_total{journey!~"audit|probe|setup",outcome!="ok"}[1m]))', "failed or refused"),
        ("sum(crowd:refused_sessions:rate1m)", "sessions refused at the cap"),
        ('sum(rate(gp_crowd_steps_suppressed_total[1m]))', "journey steps suppressed"),
    ], 12, y, w=12, unit="reqps", fill=0, description="A falling completion rate or disappearing steps must never look like success."))
    y += 8
    panels.append(series("Read / write split", [
        ('sum(rate(gp_edge_requests_total{method="GET"}[30s])) / sum(rate(gp_edge_requests_total[30s]))', "reads"),
        ('sum(rate(gp_edge_requests_total{method!="GET"}[30s])) / sum(rate(gp_edge_requests_total[30s]))', "writes"),
    ], 0, y, w=8, unit="percentunit", stack=True, fill=50, maximum=1))
    panels.append(series("Who is asking", [("sum by (who) (rate(gp_edge_requests_total[30s]))", "{{who}}")], 8, y, w=8, unit="reqps", stack=True, fill=40))
    panels.append(series("Key repetition (60 s)", [('gp_edge_shape{signal=~"repeat_ratio_(listing|detail|home)"}', "{{signal}}")],
                         16, y, w=8, unit="percentunit", maximum=1, fill=0,
                         description="1 - distinct keys / reads over 60 s. High repetition favours a cache; a long tail favours a replica."))
    y += 8
    # 4. Latency
    panels.append(row("Latency", y)); y += 1
    panels.append(series("p95 by journey", [("slo:latency_by_journey_seconds:p95_1m", "{{journey}}")], 0, y, w=8, unit="s", fill=0,
                         steps=[(None, GREEN), (0.25, RED)]))
    panels.append(series("p50 / p99 by journey", [("slo:latency_by_journey_seconds:p50_1m", "p50 {{journey}}"),
                                                  ("slo:latency_by_journey_seconds:p99_1m", "p99 {{journey}}")], 8, y, w=8, unit="s", fill=0))
    panels.append({
        "id": next_id(), "type": "heatmap", "title": "Read latency heatmap", "datasource": DS,
        "gridPos": {"x": 16, "y": y, "w": 8, "h": 8},
        "targets": [{"datasource": DS, "expr": 'sum by (le) (rate(gp_crowd_request_duration_seconds_bucket{kind="read",journey!~"audit|probe|setup"}[30s]))',
                     "format": "heatmap", "legendFormat": "{{le}}", "refId": "A"}],
        "options": {"calculate": False, "yAxis": {"unit": "s"}, "color": {"scheme": "Oranges", "mode": "scheme"},
                    "cellGap": 1, "tooltip": {"mode": "single"}},
    })
    y += 8
    # 5. Errors
    panels.append(row("Errors", y)); y += 1
    panels.append(series("Errors by journey and outcome", [
        ('sum by (journey, outcome) (rate(gp_crowd_requests_total{journey!~"audit|probe|setup",outcome=~"error_5xx|timeout|conn_error"}[30s]))', "{{journey}} {{outcome}}")],
        0, y, w=12, unit="reqps", stack=True, fill=60))
    panels.append(series("Correctness errors (never a cache miss)", [
        ("sum by (kind) (gp_crowd_correctness_errors_total)", "{{kind}}"),
        ("sum by (kind) (gp_probe_correctness_errors_total)", "prober {{kind}}")], 12, y, w=12, fill=0,
        description="A 404 for something the ledger says exists, or an old description after a confirmed edit."))
    y += 8
    # 7. Pressure gauges (filled in by the architect's signals)
    panels.append(row("Pressure", y)); y += 1
    panels.append(series("Pressure signals against thresholds", [("gp_architect_signal", "{{signal}}")], 0, y, w=16, fill=0,
                         description="Each signal the architect watches. A pressure arms when its signal stays over threshold for its sustain window."))
    panels.append(stat("Armed pressures", "count(gp_architect_pressure_armed == 1) or vector(0)", 16, y, w=8, h=8, decimals=0,
                       steps=[(None, GREEN), (1, AMBER)]))
    y += 8
    # 8. Capacity
    panels.append(row("Capacity", y)); y += 1
    panels.append(series("CPU by component against its cap", [
        ("sum by (unit) (rate(gp_unit_cpu_seconds_total[30s])) / sum by (unit) (gp_unit_cpu_limit_cores)", "{{unit}}"),
        ('sum(rate(gp_request_cpu_seconds_total[30s]))', "API handler CPU (cores)")], 0, y, w=12, unit="percentunit", fill=0,
        steps=[(None, GREEN), (0.75, RED)]))
    panels.append(series("API queue time p95", [
        ("histogram_quantile(0.95, sum by (le) (rate(gp_queue_time_seconds_bucket[30s])))", "queue p95")], 12, y, w=6, unit="s", fill=10,
        steps=[(None, GREEN), (0.025, RED)], description="Time from nginx accepting a request to the handler starting: the leading signal for api-compute."))
    panels.append(series("API instances", [
        ("count(gp_instance_start_time_seconds)", "running"),
        ('kube_horizontalpodautoscaler_status_desired_replicas or vector(0)', "desired (HPA)")], 18, y, w=6, fill=0))
    y += 8
    # 9. Data tier
    panels.append(row("Data tier", y)); y += 1
    panels.append(series("Database CPU against its cap", [
        ('sum by (unit) (rate(gp_unit_cpu_seconds_total{unit=~"db.*"}[30s])) / sum by (unit) (gp_unit_cpu_limit_cores{unit=~"db.*"})', "{{unit}}")],
        0, y, w=8, unit="percentunit", fill=10, steps=[(None, GREEN), (0.7, RED)]))
    panels.append(series("Database time: reads vs writes", [
        ('sum by (class) (rate(gp_pg_statement_seconds_total{class=~"read|write"}[30s]))', "{{class}}")], 8, y, w=8, unit="s", stack=True, fill=50))
    panels.append(series("Connections and replication", [
        ("sum by (db) (gp_pg_connections)", "connections {{db}}"),
        ("max by (db) (gp_pg_max_connections)", "max_connections {{db}}"),
        ("max by (standby) (gp_pg_replication_lag_seconds) * 1000", "lag ms {{standby}}")], 16, y, w=8, fill=0))
    y += 8
    panels.append(series("Cache", [
        ('sum(rate(gp_cache_requests_total{result="hit"}[30s])) / sum(rate(gp_cache_requests_total{result=~"hit|miss"}[30s]))', "hit ratio")],
        0, y, w=8, unit="percentunit", maximum=1, fill=10, description="Empty until a cache exists."))
    panels.append(series("Read routing", [("sum by (role, reason) (rate(gp_read_routing_total[30s]))", "{{role}} ({{reason}})")],
                         8, y, w=8, unit="reqps", stack=True, fill=40, description="Empty until a replica exists."))
    panels.append(series("Sequential scans on large tables", [
        ('sum by (table) (rate(gp_pg_table_seq_tuples_read_total{table=~"products|reviews|variants"}[30s]))', "{{table}}")],
        16, y, w=8, unit="rowsps", fill=10))
    y += 8
    # 10. Narration
    panels.append(row("Narration", y)); y += 1
    panels.append({
        "id": next_id(), "type": "annolist", "title": "What the traffic made the system do",
        "gridPos": {"x": 0, "y": y, "w": 24, "h": 10},
        "options": {"onlyFromThisDashboard": False, "onlyInTimeRange": False, "tags": ["narration"], "limit": 50,
                    "showUser": False, "showTime": True, "showTags": False, "navigateToPanel": False,
                    "navigateBefore": "10m", "navigateAfter": "10m"},
    })
    return {
        "uid": "growing-pains", "title": "Growing Pains", "tags": ["growing-pains"], "timezone": "browser",
        "editable": False, "graphTooltip": 1, "refresh": "5s", "schemaVersion": 41, "version": 1,
        "time": {"from": "now-30m", "to": "now"},
        "timepicker": {"refresh_intervals": ["5s", "10s", "30s", "1m"]},
        "annotations": {"list": [
            {"builtIn": 1, "datasource": {"type": "grafana", "uid": "-- Grafana --"}, "enable": True, "hide": True,
             "iconColor": "rgba(0, 211, 255, 1)", "name": "Annotations & Alerts", "type": "dashboard"},
            {"datasource": {"type": "grafana", "uid": "-- Grafana --"}, "enable": True, "iconColor": "#e0a030", "name": "Moves",
             "target": {"type": "tags", "tags": ["move"], "limit": 100, "matchAny": False}},
        ]},
        "panels": panels,
    }


def main() -> None:
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text(json.dumps(build(), indent=1) + "\n")
    print(f"wrote {OUT.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
