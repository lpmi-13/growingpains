// Package actuate changes the real shop on a playground, through four narrow paths from ops-01:
// the Kubernetes API, allowlisted node actions over SSH, the runtime config document, and SQL.
// Every action is idempotent, because a restarted architect repeats the current step's action.
package actuate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"

	"github.com/lpmi-13/growingpains/internal/architect"
	"github.com/lpmi-13/growingpains/internal/kube"
	"github.com/lpmi-13/growingpains/internal/runtimecfg"
)

// Config describes the playground. ops-01 reads it from /etc/gp/playground.json.
type Config struct {
	KubeServer    string `json:"kube_server"`
	KubeTokenFile string `json:"kube_token_file"`
	KubeCAFile    string `json:"kube_ca_file"`

	Nodes        map[string]string `json:"nodes"`   // VM name -> address
	SSHKey       string            `json:"ssh_key"` // the architect's key; nodes force it through the dispatcher
	SSHUser      string            `json:"ssh_user"`
	ShopImage    string            `json:"shop_image"`
	ValkeyImage  string            `json:"valkey_image"`
	PauseImage   string            `json:"pause_image"`
	AdminDSN     string            `json:"admin_dsn"`     // the primary, as the architect's administrative role
	AppDSN       string            `json:"app_dsn"`       // the primary, as the API connects to it
	ReplicaDSN   string            `json:"replica_dsn"`   // a replica, as the API connects to it: %s is the node's address
	PoolerDSN    string            `json:"pooler_dsn"`    // PgBouncer, as the API connects to it
	ConfigURL    string            `json:"config_url"`    // the runtime config document, as instances fetch it
	ClusterDNS   string            `json:"cluster_dns"`   // CoreDNS's service IP, for nginx's resolver
	LegacyServer string            `json:"legacy_server"` // the genesis process, as nginx on its own node reaches it
	LegacyAddr   string            `json:"legacy_addr"`   // the genesis process, as the architect reaches it from ops-01
	FleetHost    string            `json:"fleet_host"`    // the headless Service's DNS name
	APIPort      int               `json:"api_port"`
}

// NewPlayground connects to the playground's Kubernetes API.
func NewPlayground(cfg Config, runtime *runtimecfg.Store, manifests fs.FS, log *slog.Logger) (*Playground, error) {
	if cfg.LegacyServer == "" || cfg.LegacyAddr == "" {
		return nil, fmt.Errorf("playground config needs legacy_server (for nginx) and legacy_addr (for the architect)")
	}
	k, err := kube.New(cfg.KubeServer, cfg.KubeTokenFile, cfg.KubeCAFile)
	if err != nil {
		return nil, err
	}
	return &Playground{Cfg: cfg, Kube: k, Runtime: runtime, Manifests: manifests, Log: log, HTTP: &http.Client{Timeout: 3 * time.Second}}, nil
}

// Playground is the real actuator.
type Playground struct {
	Cfg       Config
	Kube      *kube.Client
	Runtime   *runtimecfg.Store
	Manifests fs.FS
	Log       *slog.Logger
	HTTP      *http.Client
}

const (
	dormantTaint = "growing-pains/dormant"
	dataTaint    = "growing-pains/data"
	namespace    = "shop"
)

func strs(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out
	case []string:
		return x
	case string:
		return []string{x}
	}
	return nil
}

func num(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case float64:
		return x
	}
	return 0
}

// vars are the ${name} placeholders contracts may use in params.
func (p *Playground) vars(run *architect.Run, model *architect.Model) map[string]string {
	v := map[string]string{
		"primary_dsn": p.Cfg.AppDSN,
		"pooler_dsn":  p.Cfg.PoolerDSN,
	}
	for _, pl := range run.Placed {
		switch pl.Kind {
		case "postgres-replica":
			v["replica_dsn"] = fmt.Sprintf(p.Cfg.ReplicaDSN, p.Cfg.Nodes[pl.Node])
		case "valkey":
			v["valkey_address"] = "valkey.shop.svc.cluster.local:6379"
		}
	}
	return v
}

func substitute(x any, vars map[string]string) any {
	switch t := x.(type) {
	case string:
		for k, v := range vars {
			t = strings.ReplaceAll(t, "${"+k+"}", v)
		}
		return t
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = substitute(e, vars)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range t {
			out[k] = substitute(e, vars)
		}
		return out
	}
	return x
}

// render fills a manifest template and splits it into objects.
func (p *Playground) render(name string, data map[string]any) ([]map[string]any, error) {
	src, err := fs.ReadFile(p.Manifests, name+".yaml")
	if err != nil {
		return nil, err
	}
	t, err := template.New(name).Option("missingkey=error").Parse(string(src))
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return nil, err
	}
	var objs []map[string]any
	dec := yaml.NewDecoder(&b)
	for {
		var o map[string]any
		if err := dec.Decode(&o); err != nil {
			break
		}
		if o != nil {
			objs = append(objs, o)
		}
	}
	return objs, nil
}

func (p *Playground) manifestData(params map[string]any, run *architect.Run) map[string]any {
	d := map[string]any{
		"ShopImage": p.Cfg.ShopImage, "ValkeyImage": p.Cfg.ValkeyImage, "PauseImage": p.Cfg.PauseImage,
		"DatabaseURL": p.Cfg.AppDSN, "ConfigURL": p.Cfg.ConfigURL, "Node": "", "CPU": "0", "Memory": "0",
	}
	for _, pl := range run.Placed {
		if pl.Kind == "valkey" {
			d["Node"] = pl.Node
		}
	}
	if env, ok := params["env"].(map[string]any); ok {
		if url, ok := env["GP_DATABASE_URL"].(string); ok {
			d["DatabaseURL"] = url
		}
	}
	return d
}

func (p *Playground) applyManifest(ctx context.Context, name string, data map[string]any, del bool) error {
	objs, err := p.render(name, data)
	if err != nil {
		return err
	}
	if !del {
		ns, err := p.render("namespace", nil)
		if err != nil {
			return err
		}
		objs = append(ns, objs...)
	}
	for _, o := range objs {
		if del {
			err = p.Kube.Delete(ctx, o)
		} else {
			err = p.Kube.Apply(ctx, o)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// taints returns a node's taints without the given key, plus the ones to add.
func (p *Playground) setTaints(ctx context.Context, node string, remove string, add []map[string]any) error {
	n, err := p.Kube.Get(ctx, "/api/v1/nodes/"+node)
	if err != nil {
		return err
	}
	spec, _ := n["spec"].(map[string]any)
	var keep []any
	present := map[string]bool{}
	if ts, ok := spec["taints"].([]any); ok {
		for _, t := range ts {
			tm, _ := t.(map[string]any)
			if tm["key"] == remove {
				continue
			}
			present[fmt.Sprint(tm["key"])] = true
			keep = append(keep, t)
		}
	}
	for _, t := range add {
		if !present[fmt.Sprint(t["key"])] {
			keep = append(keep, t)
		}
	}
	if keep == nil {
		keep = []any{}
	}
	return p.Kube.MergePatch(ctx, "/api/v1/nodes/"+node, map[string]any{"spec": map[string]any{"taints": keep}})
}

// ssh runs an allowlisted action on a node. The node's authorized_keys entry forces every
// command through its dispatcher, which refuses anything not on its list.
func (p *Playground) ssh(ctx context.Context, node string, stdin []byte, args ...string) (string, error) {
	addr := p.Cfg.Nodes[node]
	if addr == "" {
		return "", fmt.Errorf("unknown node %q", node)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmdArgs := []string{"-i", p.Cfg.SSHKey, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=5", p.Cfg.SSHUser + "@" + addr, "--"}
	cmd := exec.CommandContext(ctx, "ssh", append(cmdArgs, args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s on %s: %w: %s", strings.Join(args, " "), node, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// upstream renders nginx's upstream for a weighted split between the legacy process and the fleet.
// nginx weights each resolved pod separately, so the legacy weight is scaled by the pod count to
// keep the split what the step says.
func (p *Playground) upstream(legacy, fleet float64, pods int) []byte {
	var b strings.Builder
	b.WriteString("# Written by the architect's nginx-apply action.\nupstream shop {\n    zone shop 256k;\n")
	if fleet > 0 {
		fmt.Fprintf(&b, "    resolver %s valid=1s ipv6=off;\n", p.Cfg.ClusterDNS)
	}
	pods = max(pods, 1)
	if legacy > 0 {
		fmt.Fprintf(&b, "    server %s weight=%d max_fails=0;\n", p.Cfg.LegacyServer, max(1, int(legacy)*pods))
	}
	if fleet > 0 {
		fmt.Fprintf(&b, "    server %s:%d resolve weight=%d max_fails=0;\n", p.Cfg.FleetHost, p.Cfg.APIPort, max(1, int(fleet)))
	}
	b.WriteString("    keepalive 64;\n}\n")
	return []byte(b.String())
}

func (p *Playground) readyPods(ctx context.Context, app string) ([]string, error) {
	list, err := p.Kube.Get(ctx, "/api/v1/namespaces/"+namespace+"/pods?labelSelector=app%3D"+app)
	if err != nil {
		return nil, err
	}
	var ips []string
	items, _ := list["items"].([]any)
	for _, it := range items {
		pod, _ := it.(map[string]any)
		meta, _ := pod["metadata"].(map[string]any)
		if meta["deletionTimestamp"] != nil {
			continue
		}
		status, _ := pod["status"].(map[string]any)
		conds, _ := status["conditions"].([]any)
		for _, c := range conds {
			cm, _ := c.(map[string]any)
			if cm["type"] == "Ready" && cm["status"] == "True" {
				if ip, ok := status["podIP"].(string); ok && ip != "" {
					ips = append(ips, ip)
				}
			}
		}
	}
	sort.Strings(ips)
	return ips, nil
}

// Do performs an action.
func (p *Playground) Do(ctx context.Context, action string, params map[string]any, run *architect.Run, model *architect.Model) error {
	params, _ = substitute(params, p.vars(run, model)).(map[string]any)
	switch action {
	case "nodes.acquire":
		for _, node := range strs(params["nodes"]) {
			var add []map[string]any
			if n := model.Node(node); n != nil && n.Role == "data" {
				add = append(add, map[string]any{"key": dataTaint, "value": "true", "effect": "NoSchedule"})
			}
			if err := p.setTaints(ctx, node, dormantTaint, add); err != nil {
				return err
			}
			if reserve, ok := params["reserve"].(map[string]any); ok {
				objs, err := p.render("reservation", map[string]any{"Node": node, "PauseImage": p.Cfg.PauseImage,
					"CPU": fmt.Sprint(reserve["cpu"]), "Memory": fmt.Sprint(reserve["memory"])})
				if err != nil {
					return err
				}
				for _, o := range objs {
					if err := p.Kube.Apply(ctx, o); err != nil {
						return err
					}
				}
			}
		}
		return nil
	case "nodes.release":
		for _, node := range strs(params["nodes"]) {
			p.Kube.Delete(ctx, map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "reserve-" + node, "namespace": namespace}})
			if err := p.setTaints(ctx, node, "", []map[string]any{{"key": dormantTaint, "value": "true", "effect": "NoSchedule"}}); err != nil {
				return err
			}
		}
		return nil
	case "kube.apply", "kube.rollout":
		return p.applyManifest(ctx, fmt.Sprint(params["manifest"]), p.manifestData(params, run), false)
	case "kube.delete":
		return p.applyManifest(ctx, fmt.Sprint(params["manifest"]), p.manifestData(params, run), true)
	case "edge.weights":
		ips, _ := p.readyPods(ctx, "api")
		run.Vars["legacy_weight"], run.Vars["fleet_weight"] = num(params["legacy"]), num(params["fleet"])
		_, err := p.ssh(ctx, "node-01", p.upstream(num(params["legacy"]), num(params["fleet"]), len(ips)), "nginx-apply")
		return err
	case "edge.remove_legacy":
		_, err := p.ssh(ctx, "node-01", p.upstream(0, 100, 1), "nginx-apply")
		return err
	case "node.run":
		node := fmt.Sprint(params["node"])
		if node == "" || node == "<nil>" {
			// A placement the ledger chose, such as a second replica.
			for _, pl := range run.Placed {
				node = pl.Node
			}
		}
		args := append([]string{fmt.Sprint(params["script"])}, strs(params["args"])...)
		_, err := p.ssh(ctx, node, nil, args...)
		return err
	case "sql.primary":
		return p.sql(ctx, fmt.Sprint(params["statement"]), fmt.Sprint(params["slot"]))
	case "config.set":
		_, err := p.Runtime.Patch(params)
		return err
	}
	return fmt.Errorf("unknown action %q", action)
}

func (p *Playground) sql(ctx context.Context, statement, slot string) error {
	conn, err := pgx.Connect(ctx, p.Cfg.AdminDSN)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	switch statement {
	case "ensure_replication":
		_, err = conn.Exec(ctx, `SELECT pg_create_physical_replication_slot($1) WHERE NOT EXISTS
			(SELECT 1 FROM pg_replication_slots WHERE slot_name = $1)`, slot)
	case "drop_slot":
		// A slot left behind keeps WAL on the primary until its disk fills.
		_, err = conn.Exec(ctx, `SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots
			WHERE slot_name = $1 AND NOT active`, slot)
	default:
		err = fmt.Errorf("unknown statement %q", statement)
	}
	return err
}

func (p *Playground) instanceVersion(ctx context.Context, addr string) (int, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var body struct {
		Version int `json:"config_version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	return body.Version, nil
}

// instances are every API instance serving now: the legacy process while it runs, and each pod.
func (p *Playground) instances(ctx context.Context, model *architect.Model) ([]string, error) {
	var out []string
	if model.Facts["api_mode"] == "systemd" {
		out = append(out, p.Cfg.LegacyAddr)
	}
	ips, err := p.readyPods(ctx, "api")
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		out = append(out, net.JoinHostPort(ip, fmt.Sprint(p.Cfg.APIPort)))
	}
	return out, nil
}

// Ready reports whether a probe passes.
func (p *Playground) Ready(ctx context.Context, probe string, params map[string]any, run *architect.Run, model *architect.Model) (bool, string, error) {
	switch probe {
	case "nodes_ready":
		for _, node := range strs(params["nodes"]) {
			n, err := p.Kube.Get(ctx, "/api/v1/nodes/"+node)
			if err != nil {
				return false, "", err
			}
			status, _ := n["status"].(map[string]any)
			conds, _ := status["conditions"].([]any)
			ready := false
			for _, c := range conds {
				cm, _ := c.(map[string]any)
				if cm["type"] == "Ready" && cm["status"] == "True" {
					ready = true
				}
			}
			if !ready {
				return false, node + " is not Ready", nil
			}
		}
		return true, "", nil
	case "fleet_ready":
		ips, err := p.readyPods(ctx, "api")
		if err != nil {
			return false, "", err
		}
		run.Vars["ready_pods"] = len(ips)
		return len(ips) >= 2, fmt.Sprintf("%d pods ready", len(ips)), nil
	case "fleet_converged", "config_converged":
		want := p.Runtime.Version()
		addrs, err := p.instances(ctx, model)
		if err != nil {
			return false, "", err
		}
		if probe == "fleet_converged" && len(addrs) < 2 {
			return false, "waiting for pods", nil
		}
		for _, a := range addrs {
			v, err := p.instanceVersion(ctx, a)
			if err != nil {
				return false, a + ": " + err.Error(), nil
			}
			if v < want {
				return false, fmt.Sprintf("%s runs config %d of %d", a, v, want), nil
			}
			if probe == "fleet_converged" {
				// A smoke request through each pod, before any customer traffic.
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+a+"/", nil)
				resp, err := p.HTTP.Do(req)
				if err != nil {
					return false, a + ": " + err.Error(), nil
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					return false, fmt.Sprintf("%s answered %d", a, resp.StatusCode), nil
				}
			}
		}
		return true, fmt.Sprintf("%d instances on config %d", len(addrs), want), nil
	case "valkey_ready":
		ips, err := p.readyPods(ctx, "valkey")
		if err != nil {
			return false, "", err
		}
		return len(ips) >= 1, "", nil
	case "replica_streaming":
		conn, err := pgx.Connect(ctx, p.Cfg.AdminDSN)
		if err != nil {
			return false, "", err
		}
		defer conn.Close(ctx)
		var n int
		slot := ""
		if args := strs(params["args"]); len(args) > 1 {
			slot = args[1]
		}
		err = conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_replication r JOIN pg_replication_slots s ON s.active_pid = r.pid
			WHERE r.state = 'streaming' AND s.slot_name = $1`, slot).Scan(&n)
		if err != nil {
			return false, "", err
		}
		return n > 0, fmt.Sprintf("%d streaming on slot %s", n, slot), nil
	}
	return false, "", fmt.Errorf("unknown probe %q", probe)
}
