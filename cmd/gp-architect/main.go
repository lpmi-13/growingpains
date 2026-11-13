// Command gp-architect is the slow control loop on ops-01. It serves the runtime config document
// the API instances poll, its state and events for the narrator, and its metrics for Grafana.
//
//	gp-architect -prometheus http://127.0.0.1:9090 -observe   # read and arm, never move
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/lpmi-13/growingpains/contracts"
	"github.com/lpmi-13/growingpains/deploy/k8s"
	"github.com/lpmi-13/growingpains/internal/actuate"
	"github.com/lpmi-13/growingpains/internal/architect"
	"github.com/lpmi-13/growingpains/internal/archsvc"
	"github.com/lpmi-13/growingpains/internal/runtimecfg"
	"github.com/lpmi-13/growingpains/internal/signals"
)

// observer is the actuator for observe-only runs. It refuses every action.
type observer struct{}

var errObserveOnly = errors.New("observe-only: the architect may not change the shop")

func (observer) Do(context.Context, string, map[string]any, *architect.Run, *architect.Model) error {
	return errObserveOnly
}

func (observer) Ready(context.Context, string, map[string]any, *architect.Run, *architect.Model) (bool, string, error) {
	return false, "", errObserveOnly
}

func main() {
	listen := flag.String("listen", ":7000", "address for /config, /api, and /metrics")
	prom := flag.String("prometheus", "http://127.0.0.1:9090", "Prometheus to read signals from")
	statePath := flag.String("state", "/var/lib/gp-architect/state.json", "persisted state")
	eventsPath := flag.String("events", "/var/lib/gp-architect/events.jsonl", "event log")
	configPath := flag.String("config", "/var/lib/gp-architect/runtime-config.json", "runtime config document")
	grafana := flag.String("grafana", "", "Grafana URL for annotations, for example http://127.0.0.1:3000")
	grafanaUser := flag.String("grafana-user", "admin", "")
	grafanaPassword := flag.String("grafana-password", os.Getenv("GP_GRAFANA_PASSWORD"), "")
	observe := flag.Bool("observe", false, "the control run: measure and arm, never move")
	delay := flag.Duration("delay", 0, "the timing control: wait this long after each decision")
	interval := flag.Duration("interval", 5*time.Second, "loop interval")
	actuator := flag.String("actuator", "none", "how the architect changes the shop: none or playground")
	playground := flag.String("playground", "/etc/gp/playground.json", "the playground's description, for -actuator=playground")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cat, err := architect.Load(contracts.FS)
	if err != nil {
		log.Error("contracts", "err", err)
		os.Exit(1)
	}
	cfgStore, err := runtimecfg.Open(*configPath)
	if err != nil {
		log.Error("runtime config", "err", err)
		os.Exit(1)
	}
	annotations := archsvc.NewGrafana(*grafana, *grafanaUser, *grafanaPassword, log)
	events, err := archsvc.OpenLog(*eventsPath, annotations)
	if err != nil {
		log.Error("event log", "err", err)
		os.Exit(1)
	}
	cfg := architect.DefaultConfig
	cfg.Enabled, cfg.Delay = !*observe, *delay
	var act architect.Actuator = observer{}
	switch *actuator {
	case "playground":
		data, err := os.ReadFile(*playground)
		if err != nil {
			log.Error("playground", "err", err)
			os.Exit(1)
		}
		var pc actuate.Config
		if err := json.Unmarshal(data, &pc); err != nil {
			log.Error("playground", "err", err)
			os.Exit(1)
		}
		p, err := actuate.NewPlayground(pc, cfgStore, k8s.FS, log)
		if err != nil {
			log.Error("playground", "err", err)
			os.Exit(1)
		}
		act = p
	case "none":
		if cfg.Enabled {
			log.Error("no actuator: run with -observe, or -actuator=playground")
			os.Exit(2)
		}
	default:
		log.Error("unknown actuator", "actuator", *actuator)
		os.Exit(2)
	}
	a := &architect.Architect{
		Catalog: cat, Source: &signals.Prometheus{Base: *prom}, Act: act,
		Store: architect.FileStore{Path: *statePath}, Clock: architect.RealClock{}, Rec: events, Cfg: cfg, Log: log,
	}
	if err := a.Init(); err != nil {
		log.Error("state", "err", err)
		os.Exit(1)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}), archsvc.NewMetrics(a))
	mux := http.NewServeMux()
	mux.Handle("GET /config", cfgStore)
	mux.Handle("/api/", archsvc.API(a, events))
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go a.Run(ctx, *interval)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	log.Info("architect ready", "listen", *listen, "observe", *observe, "moves", len(cat.Moves), "pressures", len(cat.Pressures))
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
