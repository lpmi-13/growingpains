package actuate

import (
	"strings"
	"testing"

	"github.com/lpmi-13/growingpains/deploy/k8s"
	"github.com/lpmi-13/growingpains/internal/architect"
)

func testPlayground() *Playground {
	return &Playground{Manifests: k8s.FS, Cfg: Config{
		ShopImage: "registry.growingpains.internal/shop:1", ValkeyImage: "valkey:9.1", PauseImage: "pause:3.10",
		AppDSN: "postgresql://shop@node-01/shop", ConfigURL: "http://ops-01:7000/config",
		ClusterDNS: "10.43.0.10", LegacyServer: "127.0.0.1:8000", FleetHost: "api.shop.svc.cluster.local", APIPort: 8000,
		ReplicaDSN: "postgresql://shop@%s/shop", Nodes: map[string]string{"node-04": "172.16.0.14"},
	}}
}

func TestManifestsRender(t *testing.T) {
	p := testPlayground()
	run := &architect.Run{Placed: []architect.Resolved{{Placement: architect.Placement{Kind: "valkey"}, Node: "node-04"}}}
	for _, name := range []string{"api-fleet", "valkey", "namespace"} {
		objs, err := p.render(name, p.manifestData(nil, run))
		if err != nil || len(objs) == 0 {
			t.Fatalf("%s: %v (%d objects)", name, err, len(objs))
		}
	}
	objs, _ := p.render("api-fleet", p.manifestData(nil, run))
	kinds := []string{}
	for _, o := range objs {
		kinds = append(kinds, o["kind"].(string))
	}
	if strings.Join(kinds, ",") != "Deployment,Service,PodDisruptionBudget,HorizontalPodAutoscaler" {
		t.Fatalf("api-fleet kinds %v", kinds)
	}
	if _, err := p.render("reservation", map[string]any{"Node": "node-04", "PauseImage": "pause", "CPU": "1500m", "Memory": "1536Mi"}); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamKeepsTheSplit(t *testing.T) {
	p := testPlayground()
	conf := string(p.upstream(90, 10, 2))
	// Two pods at weight 10 each against a legacy weight of 180: 90% legacy.
	if !strings.Contains(conf, "server 127.0.0.1:8000 weight=180") || !strings.Contains(conf, "resolve weight=10") || !strings.Contains(conf, "resolver 10.43.0.10") {
		t.Fatal(conf)
	}
	if strings.Contains(string(p.upstream(0, 100, 3)), "127.0.0.1") {
		t.Fatal("legacy still in the upstream after it was removed")
	}
	if strings.Contains(string(p.upstream(100, 0, 0)), "resolve") {
		t.Fatal("the fleet is in the upstream before it exists")
	}
}

func TestSubstituteFillsPlaceholders(t *testing.T) {
	p := testPlayground()
	run := &architect.Run{Placed: []architect.Resolved{{Placement: architect.Placement{Kind: "postgres-replica"}, Node: "node-04"}}}
	params := map[string]any{"read_routing": map[string]any{"replicas": []any{"${replica_dsn}"}, "replica_pct": 10}}
	out := substitute(params, p.vars(run, architect.Genesis())).(map[string]any)
	got := out["read_routing"].(map[string]any)["replicas"].([]any)[0]
	if got != "postgresql://shop@172.16.0.14/shop" {
		t.Fatalf("got %v", got)
	}
}
