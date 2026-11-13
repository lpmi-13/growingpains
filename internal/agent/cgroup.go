package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// Cgroups reports the CPU and memory of systemd units or containers against their caps. The
// database's CPU against its quota is the signal several moves turn on (db.cpu_ratio).
type Cgroups struct {
	units map[string]string // name -> cgroup directory glob

	cpu, throttled, limit, mem, memMax *prometheus.Desc
}

// NewCgroups watches name=glob pairs, for example db=/sys/fs/cgroup/system.slice/postgresql@18-main.service.
func NewCgroups(specs []string) *Cgroups {
	c := &Cgroups{
		units:     map[string]string{},
		cpu:       prometheus.NewDesc("gp_unit_cpu_seconds_total", "CPU used by the unit", []string{"unit"}, nil),
		throttled: prometheus.NewDesc("gp_unit_throttled_seconds_total", "Time the unit was throttled by its CPU quota", []string{"unit"}, nil),
		limit:     prometheus.NewDesc("gp_unit_cpu_limit_cores", "The unit's CPU quota in cores; the machine's CPUs if it has none", []string{"unit"}, nil),
		mem:       prometheus.NewDesc("gp_unit_memory_bytes", "Memory charged to the unit", []string{"unit"}, nil),
		memMax:    prometheus.NewDesc("gp_unit_memory_limit_bytes", "The unit's memory cap, 0 if it has none", []string{"unit"}, nil),
	}
	for _, s := range specs {
		if name, glob, ok := strings.Cut(s, "="); ok {
			c.units[name] = glob
		}
	}
	return c
}

func (c *Cgroups) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.cpu, c.throttled, c.limit, c.mem, c.memMax} {
		ch <- d
	}
}

func (c *Cgroups) Collect(ch chan<- prometheus.Metric) {
	for name, glob := range c.units {
		dirs, _ := filepath.Glob(glob)
		for _, dir := range dirs {
			label := name
			if len(dirs) > 1 {
				label = name + ":" + filepath.Base(dir)
			}
			stat := keyValues(filepath.Join(dir, "cpu.stat"))
			if stat == nil {
				continue
			}
			ch <- prometheus.MustNewConstMetric(c.cpu, prometheus.CounterValue, stat["usage_usec"]/1e6, label)
			ch <- prometheus.MustNewConstMetric(c.throttled, prometheus.CounterValue, stat["throttled_usec"]/1e6, label)
			ch <- prometheus.MustNewConstMetric(c.limit, prometheus.GaugeValue, cpuLimit(dir), label)
			if v, ok := readNumber(filepath.Join(dir, "memory.current")); ok {
				ch <- prometheus.MustNewConstMetric(c.mem, prometheus.GaugeValue, v, label)
			}
			v, _ := readNumber(filepath.Join(dir, "memory.max"))
			ch <- prometheus.MustNewConstMetric(c.memMax, prometheus.GaugeValue, v, label)
		}
	}
}

func cpuLimit(dir string) float64 {
	data, err := os.ReadFile(filepath.Join(dir, "cpu.max"))
	if err == nil {
		fields := strings.Fields(string(data))
		if len(fields) == 2 && fields[0] != "max" {
			quota, err1 := strconv.ParseFloat(fields[0], 64)
			period, err2 := strconv.ParseFloat(fields[1], 64)
			if err1 == nil && err2 == nil && period > 0 {
				return quota / period
			}
		}
	}
	return float64(runtime.NumCPU())
}

func keyValues(path string) map[string]float64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]float64{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, " "); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				out[k] = f
			}
		}
	}
	return out
}

func readNumber(path string) (float64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	return f, err == nil
}
