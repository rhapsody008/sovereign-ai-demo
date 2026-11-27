package kube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeToken(t *testing.T, token string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, TokenFile: writeToken(t, "test-token"), HTTP: srv.Client()}
}

const podList = `{
  "kind": "PodList",
  "items": [
    {"metadata": {"name": "ready-1"}, "status": {"phase": "Running", "conditions": [
      {"type": "Ready", "status": "True"}]}},
    {"metadata": {"name": "ready-2"}, "status": {"phase": "Running", "conditions": [
      {"type": "Initialized", "status": "True"}, {"type": "Ready", "status": "True"}]}},
    {"metadata": {"name": "not-ready"}, "status": {"phase": "Running", "conditions": [
      {"type": "Ready", "status": "False"}]}},
    {"metadata": {"name": "no-conditions"}, "status": {"phase": "Running"}},
    {"metadata": {"name": "terminating", "deletionTimestamp": "2026-01-01T00:00:00Z"}, "status": {"phase": "Running", "conditions": [
      {"type": "Ready", "status": "True"}]}},
    {"metadata": {"name": "pending"}, "status": {"phase": "Pending", "conditions": [
      {"type": "Ready", "status": "True"}]}},
    {"metadata": {"name": "succeeded"}, "status": {"phase": "Succeeded"}}
  ]
}`

func TestCountReadyPods(t *testing.T) {
	var gotSelector, gotAuth, gotPath string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSelector = r.URL.Query().Get("labelSelector")
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(podList))
	}))

	n, err := c.CountReadyPods(context.Background(), "sovereign-ai", "app=secure-intake")
	if err != nil {
		t.Fatalf("CountReadyPods: %v", err)
	}
	if n != 2 {
		t.Errorf("ready pods = %d, want 2", n)
	}
	if gotSelector != "app=secure-intake" {
		t.Errorf("labelSelector = %q, want %q", gotSelector, "app=secure-intake")
	}
	if gotPath != "/api/v1/namespaces/sovereign-ai/pods" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer test-token")
	}
}

func TestCountReadyPodsRereadsToken(t *testing.T) {
	var auths []string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"items": []}`))
	}))
	if _, err := c.CountReadyPods(context.Background(), "ns", ""); err != nil {
		t.Fatalf("CountReadyPods: %v", err)
	}
	if err := os.WriteFile(c.TokenFile, []byte("rotated-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CountReadyPods(context.Background(), "ns", ""); err != nil {
		t.Fatalf("CountReadyPods: %v", err)
	}
	if len(auths) != 2 || auths[0] != "Bearer test-token" || auths[1] != "Bearer rotated-token" {
		t.Errorf("Authorization headers = %q, want first %q then %q", auths, "Bearer test-token", "Bearer rotated-token")
	}
}

func TestCountReadyPodsForbidden(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","message":"pods is forbidden: User \"system:serviceaccount:sovereign-ai:secure-intake\" cannot list resource \"pods\""}`))
	}))
	n, err := c.CountReadyPods(context.Background(), "sovereign-ai", "app=secure-intake")
	if err == nil {
		t.Fatal("expected an error for HTTP 403")
	}
	if n != 0 {
		t.Errorf("ready pods = %d, want 0", n)
	}
	for _, want := range []string{"403", "cannot list resource"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestInClusterRequiresEnv(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	if _, err := InCluster(); err == nil {
		t.Fatal("expected an error outside a cluster")
	}
}
