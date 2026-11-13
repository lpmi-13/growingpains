// Package kube is a small Kubernetes API client: server-side apply, delete, get, and merge
// patch over plain HTTPS. The architect needs only these, so it does without client-go.
package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client talks to one API server with a bearer token.
type Client struct {
	Server string
	Token  string
	HTTP   *http.Client
}

// New reads the token and CA bundle from files.
func New(server, tokenFile, caFile string) (*Client, error) {
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if caFile != "" {
		ca, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool.AppendCertsFromPEM(ca)
	}
	return &Client{
		Server: strings.TrimRight(server, "/"),
		Token:  strings.TrimSpace(string(token)),
		HTTP: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		}},
	}, nil
}

// Error is a non-2xx answer.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string { return fmt.Sprintf("kubernetes: %d %s", e.Status, e.Body) }

// NotFound reports whether err is a 404.
func NotFound(err error) bool {
	ke, ok := err.(*Error)
	return ok && ke.Status == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, path, contentType string, body []byte) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Server+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, &Error{Status: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	var out map[string]any
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Get reads an API path.
func (c *Client) Get(ctx context.Context, path string) (map[string]any, error) {
	return c.do(ctx, http.MethodGet, path, "", nil)
}

// MergePatch applies a JSON merge patch.
func (c *Client) MergePatch(ctx context.Context, path string, patch any) error {
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPatch, path, "application/merge-patch+json", data)
	return err
}

// plurals maps a kind to its API path segment and whether it is namespaced.
var plurals = map[string]struct {
	plural     string
	namespaced bool
}{
	"Namespace": {"namespaces", false}, "Node": {"nodes", false},
	"Pod": {"pods", true}, "Service": {"services", true}, "ConfigMap": {"configmaps", true},
	"Deployment": {"deployments", true}, "PodDisruptionBudget": {"poddisruptionbudgets", true},
	"HorizontalPodAutoscaler": {"horizontalpodautoscalers", true},
}

// Path is an object's API path, from its apiVersion, kind, namespace, and name.
func Path(obj map[string]any) (string, error) {
	apiVersion, _ := obj["apiVersion"].(string)
	kind, _ := obj["kind"].(string)
	meta, _ := obj["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	ns, _ := meta["namespace"].(string)
	p, ok := plurals[kind]
	if !ok || apiVersion == "" || name == "" {
		return "", fmt.Errorf("cannot address %s %q (%s)", kind, name, apiVersion)
	}
	prefix := "/apis/" + apiVersion
	if !strings.Contains(apiVersion, "/") {
		prefix = "/api/" + apiVersion
	}
	if p.namespaced {
		return fmt.Sprintf("%s/namespaces/%s/%s/%s", prefix, ns, p.plural, name), nil
	}
	return fmt.Sprintf("%s/%s/%s", prefix, p.plural, name), nil
}

// Apply creates or updates an object by server-side apply, owned by the architect.
func (c *Client) Apply(ctx context.Context, obj map[string]any) error {
	path, err := Path(obj)
	if err != nil {
		return err
	}
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPatch, path+"?fieldManager=gp-architect&force=true", "application/apply-patch+yaml", data)
	return err
}

// Delete removes an object. A missing object is already deleted.
func (c *Client) Delete(ctx context.Context, obj map[string]any) error {
	path, err := Path(obj)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodDelete, path, "", nil)
	if NotFound(err) {
		return nil
	}
	return err
}
