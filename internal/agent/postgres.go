package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// Postgres reports one database instance's statistics. Statement time is classed by the
// /*gp:<class>:<name>*/ tag the API puts in every statement, which gives db.read_share.
type Postgres struct {
	dsn  string
	name string
	log  *slog.Logger

	mu   sync.Mutex
	conn *pgx.Conn

	up, stmtSeconds, stmtCalls, stmtRows, stmtWAL                                             *prometheus.Desc
	seqScan, seqTup, idxScan, tupIns, tupUpd, tupHot, tupDel, liveRows, deadRows, tableBytes  *prometheus.Desc
	backends, active, lockWait, maxConn, isReplica, replLag, replBytes, slotBytes, slotActive *prometheus.Desc
	walBytes, dbBytes, commits, rollbacks                                                     *prometheus.Desc
}

// NewPostgres watches the database at dsn and labels it with name, for example "primary".
func NewPostgres(dsn, name string, log *slog.Logger) *Postgres {
	d := func(metric, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc(metric, help, append([]string{"db"}, labels...), nil)
	}
	return &Postgres{
		dsn: dsn, name: name, log: log,
		up:          d("gp_pg_up", "Whether the agent could read the database"),
		stmtSeconds: d("gp_pg_statement_seconds_total", "Execution time by statement class and name", "class", "stmt"),
		stmtCalls:   d("gp_pg_statement_calls_total", "Calls by statement class and name", "class", "stmt"),
		stmtRows:    d("gp_pg_statement_rows_total", "Rows by statement class and name", "class", "stmt"),
		stmtWAL:     d("gp_pg_statement_wal_bytes_total", "WAL bytes by statement class and name", "class", "stmt"),
		seqScan:     d("gp_pg_table_seq_scans_total", "Sequential scans", "table"),
		seqTup:      d("gp_pg_table_seq_tuples_read_total", "Rows read by sequential scans", "table"),
		idxScan:     d("gp_pg_table_index_scans_total", "Index scans", "table"),
		tupIns:      d("gp_pg_table_inserts_total", "Rows inserted", "table"),
		tupUpd:      d("gp_pg_table_updates_total", "Rows updated", "table"),
		tupHot:      d("gp_pg_table_hot_updates_total", "Rows updated in place (HOT)", "table"),
		tupDel:      d("gp_pg_table_deletes_total", "Rows deleted", "table"),
		liveRows:    d("gp_pg_table_live_rows", "Estimated live rows", "table"),
		deadRows:    d("gp_pg_table_dead_rows", "Estimated dead rows", "table"),
		tableBytes:  d("gp_pg_table_bytes", "Table size with indexes and TOAST", "table"),
		backends:    d("gp_pg_connections", "Client backends"),
		active:      d("gp_pg_active_backends", "Client backends running a statement"),
		lockWait:    d("gp_pg_lock_waiting_backends", "Backends waiting on a lock"),
		maxConn:     d("gp_pg_max_connections", "max_connections"),
		isReplica:   d("gp_pg_is_replica", "1 on a streaming replica"),
		replLag:     d("gp_pg_replication_lag_seconds", "Replay lag: per standby on a primary, of itself on a replica", "standby"),
		replBytes:   d("gp_pg_replication_lag_bytes", "WAL not yet replayed by a standby", "standby"),
		slotBytes:   d("gp_pg_slot_retained_wal_bytes", "WAL a replication slot keeps on the primary", "slot"),
		slotActive:  d("gp_pg_slot_active", "1 if a replication slot has a consumer", "slot"),
		walBytes:    d("gp_pg_wal_bytes_total", "WAL generated"),
		dbBytes:     d("gp_pg_database_bytes", "Database size"),
		commits:     d("gp_pg_commits_total", "Committed transactions"),
		rollbacks:   d("gp_pg_rollbacks_total", "Rolled-back transactions"),
	}
}

func (p *Postgres) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(p, ch)
}

func (p *Postgres) Collect(ch chan<- prometheus.Metric) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.collect(ctx, ch); err != nil {
		p.log.Warn("postgres scrape failed", "db", p.name, "err", err)
		if p.conn != nil {
			p.conn.Close(context.Background())
			p.conn = nil
		}
		ch <- prometheus.MustNewConstMetric(p.up, prometheus.GaugeValue, 0, p.name)
		return
	}
	ch <- prometheus.MustNewConstMetric(p.up, prometheus.GaugeValue, 1, p.name)
}

const statementsSQL = `
SELECT coalesce(substring(query from '/\*gp:([a-z]+):'), 'other') AS class,
       coalesce(substring(query from '/\*gp:[a-z]+:([a-z_]+)\*/'), 'other') AS stmt,
       sum(calls)::float8, sum(total_exec_time)::float8 / 1000, sum(rows)::float8, sum(wal_bytes)::float8
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
GROUP BY 1, 2`

const tablesSQL = `
SELECT relname, coalesce(seq_scan, 0)::float8, coalesce(seq_tup_read, 0)::float8, coalesce(idx_scan, 0)::float8,
       n_tup_ins::float8, n_tup_upd::float8, n_tup_hot_upd::float8, n_tup_del::float8,
       n_live_tup::float8, n_dead_tup::float8, pg_total_relation_size(relid)::float8
FROM pg_stat_user_tables`

const activitySQL = `
SELECT count(*) FILTER (WHERE backend_type = 'client backend')::float8,
       count(*) FILTER (WHERE backend_type = 'client backend' AND state = 'active')::float8,
       count(*) FILTER (WHERE wait_event_type = 'Lock')::float8,
       current_setting('max_connections')::float8,
       pg_is_in_recovery()
FROM pg_stat_activity`

func (p *Postgres) collect(ctx context.Context, ch chan<- prometheus.Metric) error {
	if p.conn == nil {
		conn, err := pgx.Connect(ctx, p.dsn)
		if err != nil {
			return err
		}
		p.conn = conn
	}
	c := p.conn
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, append([]string{p.name}, labels...)...)
	}
	counter := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v, append([]string{p.name}, labels...)...)
	}

	rows, err := c.Query(ctx, statementsSQL)
	if err != nil {
		return err
	}
	for rows.Next() {
		var class, stmt string
		var calls, secs, nrows, wal float64
		if err := rows.Scan(&class, &stmt, &calls, &secs, &nrows, &wal); err != nil {
			rows.Close()
			return err
		}
		counter(p.stmtCalls, calls, class, stmt)
		counter(p.stmtSeconds, secs, class, stmt)
		counter(p.stmtRows, nrows, class, stmt)
		counter(p.stmtWAL, wal, class, stmt)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = c.Query(ctx, tablesSQL)
	if err != nil {
		return err
	}
	for rows.Next() {
		var t string
		var v [10]float64
		if err := rows.Scan(&t, &v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6], &v[7], &v[8], &v[9]); err != nil {
			rows.Close()
			return err
		}
		counter(p.seqScan, v[0], t)
		counter(p.seqTup, v[1], t)
		counter(p.idxScan, v[2], t)
		counter(p.tupIns, v[3], t)
		counter(p.tupUpd, v[4], t)
		counter(p.tupHot, v[5], t)
		counter(p.tupDel, v[6], t)
		gauge(p.liveRows, v[7], t)
		gauge(p.deadRows, v[8], t)
		gauge(p.tableBytes, v[9], t)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	var backends, active, lockWait, maxConn float64
	var replica bool
	if err := c.QueryRow(ctx, activitySQL).Scan(&backends, &active, &lockWait, &maxConn, &replica); err != nil {
		return err
	}
	gauge(p.backends, backends)
	gauge(p.active, active)
	gauge(p.lockWait, lockWait)
	gauge(p.maxConn, maxConn)
	gauge(p.isReplica, b2f(replica))

	if replica {
		var lag float64
		err := c.QueryRow(ctx, `SELECT CASE WHEN pg_last_wal_receive_lsn() = pg_last_wal_replay_lsn() THEN 0
			ELSE coalesce(extract(epoch FROM now() - pg_last_xact_replay_timestamp()), 0) END::float8`).Scan(&lag)
		if err != nil {
			return err
		}
		gauge(p.replLag, lag, "self")
	} else {
		rows, err = c.Query(ctx, `SELECT application_name, coalesce(extract(epoch FROM replay_lag), 0)::float8,
			coalesce(pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn), 0)::float8 FROM pg_stat_replication`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name string
			var lag, bytes float64
			if err := rows.Scan(&name, &lag, &bytes); err != nil {
				rows.Close()
				return err
			}
			gauge(p.replLag, lag, name)
			gauge(p.replBytes, bytes, name)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = c.Query(ctx, `SELECT slot_name, active,
			coalesce(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn), 0)::float8 FROM pg_replication_slots`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var slot string
			var on bool
			var bytes float64
			if err := rows.Scan(&slot, &on, &bytes); err != nil {
				rows.Close()
				return err
			}
			gauge(p.slotBytes, bytes, slot)
			gauge(p.slotActive, b2f(on), slot)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		var wal float64
		if err := c.QueryRow(ctx, `SELECT wal_bytes::float8 FROM pg_stat_wal`).Scan(&wal); err != nil {
			return err
		}
		counter(p.walBytes, wal)
	}

	var size, commits, rollbacks float64
	if err := c.QueryRow(ctx, `SELECT pg_database_size(current_database())::float8, xact_commit::float8, xact_rollback::float8
		FROM pg_stat_database WHERE datname = current_database()`).Scan(&size, &commits, &rollbacks); err != nil {
		return err
	}
	gauge(p.dbBytes, size)
	counter(p.commits, commits)
	counter(p.rollbacks, rollbacks)
	return nil
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
