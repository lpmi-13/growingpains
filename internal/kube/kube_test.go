package kube

import "testing"

func TestPath(t *testing.T) {
	cases := map[string]map[string]any{
		"/apis/apps/v1/namespaces/shop/deployments/api":                     {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "api", "namespace": "shop"}},
		"/api/v1/namespaces/shop":                                           {"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "shop"}},
		"/api/v1/namespaces/shop/services/api":                              {"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "api", "namespace": "shop"}},
		"/apis/autoscaling/v2/namespaces/shop/horizontalpodautoscalers/api": {"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscaler", "metadata": map[string]any{"name": "api", "namespace": "shop"}},
	}
	for want, obj := range cases {
		got, err := Path(obj)
		if err != nil || got != want {
			t.Errorf("Path = %q, %v; want %q", got, err, want)
		}
	}
}
