// Package signals is the architect's fixed allowlist of what it may measure.
//
// Every signal is PromQL over the shop's own telemetry (the API, the node agents, and the
// edge) or the judge's aggregated SLO burn. None may read the crowd's own metrics, the
// storyline, or a persona label; signals_test.go fails the build if one does. The narrator
// reads the same signals, so it never says anything the system could not have known.
package signals

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Definition is one allowlisted signal.
type Definition struct {
	Name        string // dotted, as the plan names it, for example api.cpu_ratio
	Query       string
	Unit        string // ratio, seconds, rows_per_second, bytes_per_second, count
	Description string
	// Optional signals describe components or events that may not exist yet, such as a replica's
	// lag or a cache's hit ratio. An empty result means Default. For every other signal, an
	// empty result means unknown, and unknown is never healthy.
	Optional bool
	Default  float64
}

// dbCPU is the busiest database instance's CPU as a ratio of its quota.
const dbCPU = `max(sum by (unit) (rate(gp_unit_cpu_seconds_total{unit=~"db-.*"}[30s])) / sum by (unit) (gp_unit_cpu_limit_cores{unit=~"db-.*"}))`

// All is the allowlist.
var All = []Definition{
	{Name: "api.cpu_ratio", Unit: "ratio",
		Description: "API CPU against its ceiling, across every instance",
		Query:       `sum(rate(process_cpu_seconds_total{job="api"}[30s])) / sum(gp_cpu_limit_cores{job="api"})`},
	{Name: "api.queue_time_p95", Unit: "seconds",
		Description: "Time since nginx accepted a request, before the handler starts: the leading signal for api-compute",
		Query:       `histogram_quantile(0.95, sum by (le) (rate(gp_queue_time_seconds_bucket{job="api"}[30s])))`},
	{Name: "api.cpu_per_request", Unit: "seconds",
		Description: "API CPU per request, which says how much a leaner implementation would save",
		Query:       `sum(rate(process_cpu_seconds_total{job="api"}[1m])) / sum(rate(gp_http_requests_total{job="api"}[1m]))`},
	{Name: "api.side_effect_share", Optional: true, Unit: "ratio",
		Description: "API CPU spent in thumbnailing and notifications",
		Query:       `(sum(rate(gp_span_cpu_seconds_total{job="api",span="side_effect"}[1m])) or vector(0)) / sum(rate(process_cpu_seconds_total{job="api"}[1m]))`},
	{Name: "api.statements_per_request", Unit: "count",
		Description: "SQL statements per request on the busiest route",
		Query:       `max(sum by (route) (rate(gp_db_statements_total{job="api"}[1m])) / sum by (route) (rate(gp_http_requests_total{job="api"}[1m])))`},
	{Name: "api.top_client_share", Unit: "ratio",
		Description: "Share of requests from the busiest client key", Query: `max(gp_edge_shape{signal="top_client_share"})`},
	{Name: "api.instances", Unit: "count",
		Description: "API instances reporting", Query: `count(gp_instance_start_time_seconds{job="api"})`},
	{Name: "api.config_version_min", Unit: "count",
		Description: "The oldest runtime config version any API instance runs", Query: `min(gp_config_version{job="api"})`},
	{Name: "api.error_ratio", Optional: true, Unit: "ratio",
		Description: "5xx share of API responses",
		Query:       `(sum(rate(gp_edge_requests_total{code="5xx"}[30s])) or vector(0)) / sum(rate(gp_edge_requests_total[30s]))`},
	{Name: "api.fleet_error_ratio", Optional: true, Unit: "ratio",
		Description: "5xx share of responses from API pods, not the legacy process: a migration's own errors",
		Query: `sum(rate(gp_edge_upstream_requests_total{code="5xx",upstream!~"127\\.0\\.0\\.1:.*|legacy.*"}[30s]))
		  / sum(rate(gp_edge_upstream_requests_total{upstream!~"127\\.0\\.0\\.1:.*|legacy.*"}[30s]))`},
	{Name: "traffic.rate", Unit: "requests_per_second",
		Description: "Requests through the front door", Query: `sum(rate(gp_edge_requests_total[30s]))`},
	{Name: "traffic.listing_share", Unit: "ratio",
		Description: "Listing pages as a share of requests",
		Query:       `sum(rate(gp_edge_requests_total{route="listing"}[1m])) / sum(rate(gp_edge_requests_total[1m]))`},
	{Name: "traffic.detail_share", Unit: "ratio",
		Description: "Product pages as a share of requests",
		Query:       `sum(rate(gp_edge_requests_total{route="detail"}[1m])) / sum(rate(gp_edge_requests_total[1m]))`},
	{Name: "traffic.write_share", Unit: "ratio",
		Description: "Writes as a share of requests",
		Query:       `sum(rate(gp_edge_requests_total{method!="GET"}[1m])) / sum(rate(gp_edge_requests_total[1m]))`},
	{Name: "db.cpu_ratio", Unit: "ratio",
		Description: "CPU of the busiest database instance against its quota", Query: dbCPU},
	{Name: "db.primary_cpu_ratio", Unit: "ratio",
		Description: "CPU of the primary against its quota",
		Query:       `sum(rate(gp_unit_cpu_seconds_total{unit="db-primary"}[30s])) / sum(gp_unit_cpu_limit_cores{unit="db-primary"})`},
	{Name: "db.read_cpu_ratio", Unit: "ratio",
		Description: "Database CPU spent on reads, across every instance, against their combined quota",
		Query: `(sum(rate(gp_unit_cpu_seconds_total{unit=~"db-.*"}[30s])) / sum(gp_unit_cpu_limit_cores{unit=~"db-.*"}))
		  * (sum(rate(gp_pg_statement_seconds_total{class="read"}[1m])) / sum(rate(gp_pg_statement_seconds_total{class=~"read|write"}[1m])))`},
	{Name: "db.read_share", Unit: "ratio",
		Description: "Reads as a share of database execution time, by the API's statement tags",
		Query:       `sum(rate(gp_pg_statement_seconds_total{class="read"}[1m])) / sum(rate(gp_pg_statement_seconds_total{class=~"read|write"}[1m]))`},
	{Name: "db.search_share", Optional: true, Unit: "ratio",
		Description: "Search statements as a share of database execution time",
		Query:       `(sum(rate(gp_pg_statement_seconds_total{stmt="search"}[1m])) or vector(0)) / sum(rate(gp_pg_statement_seconds_total{class=~"read|write"}[1m]))`},
	{Name: "db.seqscan_rows", Unit: "rows_per_second",
		Description: "Rows read by sequential scans of the large tables on the primary",
		Query:       `sum(rate(gp_pg_table_seq_tuples_read_total{db="primary",table=~"products|reviews|variants"}[1m]))`},
	{Name: "db.connections_ratio", Unit: "ratio",
		Description: "Backends against max_connections on the primary",
		Query:       `max(gp_pg_connections{db="primary"} / gp_pg_max_connections{db="primary"})`},
	{Name: "db.lock_wait_share", Unit: "ratio",
		Description: "Share of active backends waiting on a lock",
		Query:       `sum(avg_over_time(gp_pg_lock_waiting_backends{db="primary"}[1m])) / clamp_min(sum(avg_over_time(gp_pg_active_backends{db="primary"}[1m])), 1)`},
	{Name: "db.blob_bytes_rate", Optional: true, Unit: "bytes_per_second",
		Description: "Growth of the images table",
		Query:       `max(deriv(gp_pg_table_bytes{db="primary",table="images"}[2m]))`},
	{Name: "db.repl_lag", Optional: true, Unit: "seconds",
		Description: "The worst replica's replay lag",
		Query:       `max(gp_pg_replication_lag_seconds)`},
	{Name: "writes.seller_spread", Unit: "ratio",
		Description: "The top 1% of sellers' share of writes", Query: `max(gp_edge_shape{signal="seller_top1pct_share"})`},
	{Name: "reads.repeat_ratio", Unit: "ratio",
		Description: "1 - distinct keys / reads over 60 s, for cacheable listing and product pages",
		Query:       `max(gp_edge_shape{signal="repeat_ratio_cacheable"})`},
	{Name: "reads.anonymous_share", Optional: true, Unit: "ratio",
		Description: "Cacheable GETs without a session",
		Query: `(sum(rate(gp_edge_requests_total{method="GET",who="anon",route=~"home|listing|detail"}[1m])) or vector(0))
		  / sum(rate(gp_edge_requests_total{method="GET",route=~"home|listing|detail"}[1m]))`},
	{Name: "reads.token_share", Unit: "ratio",
		Description: "Reads carrying a read-your-writes token", Query: `max(gp_edge_shape{signal="token_read_share"})`},
	{Name: "cache.hit_ratio", Optional: true, Unit: "ratio",
		Description: "Cache hits against lookups",
		Query:       `sum(rate(gp_cache_requests_total{result="hit"}[1m])) / sum(rate(gp_cache_requests_total{result=~"hit|miss"}[1m]))`},
	{Name: "search.prefix_share", Unit: "ratio", Query: `max(gp_edge_shape{signal="search_prefix_share"})`,
		Description: "Searches that extend the session's previous query within a second"},
	{Name: "search.zero_result_rate", Unit: "ratio", Query: `max(gp_edge_shape{signal="search_zero_result_rate"})`,
		Description: "Searches with no results"},
	{Name: "search.requery_rate", Unit: "ratio", Query: `max(gp_edge_shape{signal="search_requery_rate"})`,
		Description: "Searches repeated with a different spelling within 5 s"},
	{Name: "search.facet_share", Unit: "ratio", Query: `max(gp_edge_shape{signal="search_facet_share"})`,
		Description: "Searches with filters or facets"},
	{Name: "slo.burn_1m", Optional: true, Unit: "ratio", Query: `max(slo:burn_rate:1m)`,
		Description: "Error ratio over the last minute against the 0.5% the SLO allows"},
	{Name: "slo.burn_5m", Optional: true, Unit: "ratio", Query: `max(slo:burn_rate:5m)`,
		Description: "Error ratio over five minutes against the 0.5% the SLO allows"},
	{Name: "slo.budget_remaining", Optional: true, Unit: "ratio", Default: 1, Query: `max(slo:error_budget:remaining_ratio)`,
		Description: "Share of the run's error budget left"},
}

// ByName returns a definition.
func ByName(name string) (Definition, bool) {
	for _, d := range All {
		if d.Name == name {
			return d, true
		}
	}
	return Definition{}, false
}

// Values is one reading of every signal. Missing signals are absent, never zero: unknown is
// never healthy, so callers must decide what an absent value means.
type Values map[string]float64

// Get returns a signal's value and whether it was measured.
func (v Values) Get(name string) (float64, bool) {
	x, ok := v[name]
	return x, ok
}

// Source reads every signal at once. The architect reads Prometheus; the twin reads a model.
type Source interface {
	Read(ctx context.Context) (Values, error)
}

// Prometheus reads the allowlist from a Prometheus server.
type Prometheus struct {
	Base   string
	Client *http.Client
}

// Read queries every definition concurrently.
func (p *Prometheus) Read(ctx context.Context) (Values, error) {
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second}
	}
	out := Values{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	for _, d := range All {
		wg.Add(1)
		go func(d Definition) {
			defer wg.Done()
			v, ok, err := instant(ctx, client, p.Base, d.Query)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", d.Name, err)
				}
			case ok:
				out[d.Name] = v
			case d.Optional:
				out[d.Name] = d.Default
			}
		}(d)
	}
	wg.Wait()
	return out, firstErr
}

func instant(ctx context.Context, client *http.Client, base, query string) (float64, bool, error) {
	u := base + "/api/v1/query?" + url.Values{"query": {query}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Value [2]any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, false, err
	}
	if body.Status != "success" {
		return 0, false, fmt.Errorf("prometheus: %s", body.Error)
	}
	if len(body.Data.Result) == 0 {
		return 0, false, nil
	}
	s, _ := body.Data.Result[0].Value[1].(string)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false, nil
	}
	return v, true, nil
}
