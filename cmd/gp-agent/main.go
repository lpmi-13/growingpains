// Command gp-agent is the per-node exporter. It reports cgroup CPU against each unit's quota,
// PostgreSQL statistics for any local database instances, and, on the edge node, traffic-shape
// signals from nginx's request log. The architect reads these, never the crowd's metrics.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/lpmi-13/growingpains/internal/agent"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	listen := flag.String("listen", ":9102", "metrics address")
	edge := flag.String("edge-syslog", "", "UDP address for nginx's syslog stream, for example 127.0.0.1:5140")
	var units, dbs list
	flag.Var(&units, "unit", "name=cgroup-glob to report, repeatable")
	flag.Var(&dbs, "db", "name=dsn of a local database instance, repeatable")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	if len(units) > 0 {
		reg.MustRegister(agent.NewCgroups(units))
	}
	for _, spec := range dbs {
		name, dsn, _ := strings.Cut(spec, "=")
		reg.MustRegister(agent.NewPostgres(dsn, name, log))
	}
	if *edge != "" {
		e := agent.NewEdge(reg, log)
		go func() {
			for {
				if err := e.Listen(*edge); err != nil {
					log.Error("edge listener", "err", err)
					time.Sleep(time.Second)
				}
			}
		}()
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	log.Info("agent ready", "listen", *listen, "units", units.String(), "edge", *edge)
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
