// Command gp-crowd is the outside world: seeded shoppers, sellers, and visitors.
//
// Before Start it sends genesis traffic. POST /start begins the storyline from a seed. It serves
// its client-side measurements on /metrics for the judge's recording rules.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/lpmi-13/growingpains/internal/catalog"
	"github.com/lpmi-13/growingpains/internal/crowd"
)

func main() {
	var cfg crowd.Config
	listen := flag.String("listen", "127.0.0.1:9101", "control and metrics address")
	flag.StringVar(&cfg.Base, "shop", "http://127.0.0.1:8080", "the shop's front door")
	flag.IntVar(&cfg.MaxInflight, "max-inflight", 2000, "requests in flight before arrivals are refused")
	flag.IntVar(&cfg.Sellers, "sellers", 40, "seller actors besides the sellers of hot products")
	flag.StringVar(&cfg.Storyline, "storyline", "word-gets-out", "storyline family")
	flag.StringVar(&cfg.StateFile, "state", "", "file that records a started run, so a restart resumes it")
	autostart := flag.Duration("autostart", 0, "start on its own after this long, if nobody presses Start (0: never)")
	seedFlag := flag.Uint64("seed", 0, "seed for -autostart; 0 picks one from the clock")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m := crowd.NewMetrics(reg)
	m.RegisterRun(reg)
	ix := catalog.Build(catalog.Default)
	engine := crowd.New(cfg, ix, m, log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go engine.Run(ctx)

	start := func(seed uint64) error {
		if seed == 0 {
			seed = uint64(time.Now().UnixNano()) % 1_000_000
		}
		return engine.Start(ctx, seed)
	}
	if *autostart > 0 {
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(*autostart):
			}
			// A fresh playground may still be booting. The crowd may see only the shop's
			// public HTTP interface, so wait for that interface before starting the run.
			client := &http.Client{Timeout: 3 * time.Second}
			tick := time.NewTicker(5 * time.Second)
			defer tick.Stop()
			for {
				if engine.Status().Started {
					return
				}
				resp, err := client.Get(cfg.Base + "/")
				if err == nil {
					resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						if err := start(*seedFlag); err != nil && !engine.Status().Started {
							log.Warn("autostart", "err", err)
						} else {
							return
						}
					}
				}
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
				}
			}
		}()
	}

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(engine.Status())
	})
	// The storyline is revealed only for the run report.
	mux.HandleFunc("GET /storyline", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(engine.Story())
	})
	mux.HandleFunc("POST /start", func(w http.ResponseWriter, r *http.Request) {
		seed, _ := strconv.ParseUint(r.URL.Query().Get("seed"), 10, 64)
		if err := start(seed); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(engine.Status())
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	log.Info("crowd ready", "listen", *listen, "shop", cfg.Base, "storyline", cfg.Storyline)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
