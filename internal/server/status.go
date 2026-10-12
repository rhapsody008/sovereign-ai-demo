package server

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"
)

type naiStatus struct {
	Configured bool   `json:"configured"`
	Connected  bool   `json:"connected"`
	BaseURL    string `json:"baseUrl,omitempty"`
	Model      string `json:"model"`
	Source     string `json:"source"`
	LatencyMs  int64  `json:"latencyMs"`
	Error      string `json:"error"`
}

type storageStatus struct {
	Reachable   bool   `json:"reachable"`
	Backend     string `json:"backend"`
	Bucket      string `json:"bucket"`
	Endpoint    string `json:"endpoint"`
	RecordCount int    `json:"recordCount"`
	LatencyMs   int64  `json:"latencyMs"`
	Error       string `json:"error"`
}

// statusCache refreshes NAI and storage probes in the background every 5s so
// /api/status always answers instantly with the last known values.
type statusCache struct {
	s       *Server
	mu      sync.RWMutex
	n       naiStatus
	st      storageStatus
	kick    chan struct{}
	started time.Time
}

func newStatusCache(s *Server) *statusCache {
	return &statusCache{
		s:       s,
		kick:    make(chan struct{}, 1),
		started: time.Now(),
		st:      storageStatus{Backend: s.store.Backend(), Error: "checking…"},
		n:       naiStatus{Error: "checking…"},
	}
}

func (c *statusCache) run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); c.probeStorage(ctx) }()
		go func() { defer wg.Done(); c.probeNAI(ctx) }()
		wg.Wait()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.kick:
		}
	}
}

func (c *statusCache) probeStorage(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	st := storageStatus{Backend: c.s.store.Backend(), Bucket: c.s.cfg.S3Bucket, Endpoint: c.s.cfg.S3Endpoint}
	start := time.Now()
	err := c.s.store.Ping(ctx)
	if err == nil {
		st.RecordCount, err = c.s.store.CountRecords(ctx)
	}
	st.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Reachable = true
	}
	c.mu.Lock()
	c.st = st
	c.mu.Unlock()
}

func (c *statusCache) probeNAI(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	n := c.s.resolveNAI(ctx)
	ns := naiStatus{Configured: n.Source != "none", BaseURL: n.BaseURL, Model: n.Model, Source: n.Source}
	if ns.Configured {
		_, lat, err := c.s.nai.ListModels(ctx, n.Config)
		ns.LatencyMs = lat.Milliseconds()
		if err != nil {
			ns.Error = err.Error()
		} else {
			ns.Connected = true
		}
	}
	c.mu.Lock()
	c.n = ns
	c.mu.Unlock()
}

func (c *statusCache) refreshNAI() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

func (c *statusCache) markNAIOK(model string, lat time.Duration) {
	c.mu.Lock()
	c.n.Connected, c.n.Error, c.n.Model, c.n.LatencyMs = true, "", model, lat.Milliseconds()
	c.mu.Unlock()
}

func (c *statusCache) markNAIError(err error) {
	c.mu.Lock()
	c.n.Connected, c.n.Error = false, err.Error()
	c.mu.Unlock()
}

func (c *statusCache) bumpRecords() {
	c.mu.Lock()
	c.st.RecordCount++
	c.mu.Unlock()
}

func (c *statusCache) storage() storageStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.st
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.status.mu.RLock()
	n, st := s.status.n, s.status.st
	s.status.mu.RUnlock()

	cfg := s.cfg
	// Cluster tile is demo-only: values come from env, no Kubernetes API calls.
	cluster := map[string]any{"mode": "local", "healthy": true, "error": ""}
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		cluster = map[string]any{
			"mode": "in-cluster", "healthy": true,
			"nodesReady": cfg.FakeNodesTotal, "nodesTotal": cfg.FakeNodesTotal,
			"podsReady": cfg.FakePodsReady, "hpaCurrent": cfg.FakePodsReady, "hpaMax": cfg.FakeHPAMax,
			"namespace": cfg.PodNamespace, "error": "",
		}
	}
	writeJSON(w, 200, map[string]any{
		"connection": map[string]any{
			"host": requestHost(r), "cluster": cfg.ClusterName, "displayName": cfg.ClusterDisplayName,
			"role": cfg.ClusterRole, "bannerColor": cfg.BannerColor, "pod": cfg.PodName, "node": cfg.NodeName,
		},
		"cluster": cluster,
		"nai":     n,
		"storage": st,
		"uptimeS": int(time.Since(s.status.started).Seconds()),
		"version": s.version,
	})
}
