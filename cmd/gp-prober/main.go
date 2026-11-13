// Command gp-prober sends low, constant synthetic checks for each journey and for data freshness.
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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/lpmi-13/growingpains/internal/prober"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9103", "metrics address")
	shop := flag.String("shop", "http://127.0.0.1:8080", "the shop's front door")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	reg := prometheus.NewRegistry()
	p := prober.New(*shop, reg, log)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		if p.Setup(ctx) == nil {
			p.Run(ctx)
		}
	}()
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); server.Close() }()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
