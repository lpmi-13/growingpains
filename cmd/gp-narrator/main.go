// Command gp-narrator serves the narrator page: Start, the run clock, the written narration, and
// the decision records, live over SSE. It also writes narration annotations to Grafana.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lpmi-13/growingpains/contracts"
	"github.com/lpmi-13/growingpains/internal/architect"
	"github.com/lpmi-13/growingpains/internal/archsvc"
	"github.com/lpmi-13/growingpains/internal/narrator"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	listen := flag.String("listen", ":8080", "page address")
	arch := flag.String("architect", "http://127.0.0.1:7000", "the architect's API")
	crowd := flag.String("crowd", "http://127.0.0.1:9101", "the crowd's control API")
	prom := flag.String("prometheus", "http://127.0.0.1:9090", "Prometheus")
	grafana := flag.String("grafana", "", "Grafana URL for narration annotations")
	grafanaUser := flag.String("grafana-user", "admin", "")
	grafanaPassword := flag.String("grafana-password", os.Getenv("GP_GRAFANA_PASSWORD"), "")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cat, err := architect.Load(contracts.FS)
	if err != nil {
		log.Error("contracts", "err", err)
		os.Exit(1)
	}
	var ann narrator.Annotator
	if *grafana != "" {
		ann = archsvc.NewGrafana(*grafana, *grafanaUser, *grafanaPassword, log)
	}
	n := narrator.New(narrator.Sources{Architect: *arch, Crowd: *crowd, Prometheus: *prom}, cat, ann, log)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go n.Run(ctx, 2*time.Second)
	registry := prometheus.NewRegistry()
	registry.MustRegister(narrator.NewMetrics(n))
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.Handle("/", n.Handler())
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); server.Close() }()
	log.Info("narrator ready", "listen", *listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
