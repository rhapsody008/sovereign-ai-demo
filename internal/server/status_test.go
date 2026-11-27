package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rhapsody008/sovereign-ai-demo/internal/config"
	"github.com/rhapsody008/sovereign-ai-demo/internal/kube"
)

// testServer builds only what handleStatus reads: config and the status cache.
func testServer(pods int, podsErr string) *Server {
	return &Server{
		cfg: &config.Config{
			PodNamespace: "sovereign-ai", PodSelector: "app=secure-intake",
			FakeNodesTotal: 3, FakeHPAMax: 8,
		},
		version: "test",
		status:  &statusCache{pods: pods, podsErr: podsErr, started: time.Now()},
	}
}

func clusterBlock(t *testing.T, s *Server) map[string]any {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleStatus(rr, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rr.Code)
	}
	var body struct {
		Cluster map[string]any `json:"cluster"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body.Cluster
}

func TestStatusClusterLocalMode(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	c := clusterBlock(t, testServer(0, ""))
	if c["mode"] != "local" {
		t.Errorf("mode = %v, want local", c["mode"])
	}
	if _, ok := c["podsReady"]; ok {
		t.Error("local mode must not expose podsReady")
	}
}

func TestStatusClusterLivePods(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	c := clusterBlock(t, testServer(3, ""))
	if c["mode"] != "in-cluster" {
		t.Errorf("mode = %v, want in-cluster", c["mode"])
	}
	if c["podsReady"] != float64(3) {
		t.Errorf("podsReady = %v, want 3", c["podsReady"])
	}
	if c["healthy"] != true || c["error"] != "" {
		t.Errorf("healthy = %v, error = %q; want true and empty", c["healthy"], c["error"])
	}
}

func TestStatusClusterProbeError(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	c := clusterBlock(t, testServer(3, "pods is forbidden"))
	if c["healthy"] != false {
		t.Errorf("healthy = %v, want false", c["healthy"])
	}
	if c["error"] != "pods is forbidden" {
		t.Errorf("error = %v, want the probe error", c["error"])
	}
}

func TestProbePodsClearsError(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("test-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	fail := true
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"pods is forbidden"}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"secure-intake-1"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}`))
	}))
	defer srv.Close()

	c := &statusCache{
		s:       &Server{cfg: &config.Config{PodNamespace: "sovereign-ai", PodSelector: "app=secure-intake"}},
		kube:    &kube.Client{BaseURL: srv.URL, TokenFile: token, HTTP: srv.Client()},
		podsErr: "checking…",
	}
	c.probePods(context.Background())
	if !strings.Contains(c.podsErr, "403") {
		t.Fatalf("podsErr = %q, want the 403 error", c.podsErr)
	}

	fail = false
	c.probePods(context.Background())
	if c.podsErr != "" {
		t.Errorf("podsErr = %q, want it cleared after a successful probe", c.podsErr)
	}
	if c.pods != 1 {
		t.Errorf("pods = %d, want 1", c.pods)
	}
}
