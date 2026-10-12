// Package server wires the HTTP API, the embedded UI and the status cache.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rhapsody008/sovereign-ai-demo/internal/config"
	"github.com/rhapsody008/sovereign-ai-demo/internal/imaging"
	"github.com/rhapsody008/sovereign-ai-demo/internal/nai"
	"github.com/rhapsody008/sovereign-ai-demo/internal/pii"
	"github.com/rhapsody008/sovereign-ai-demo/internal/store"
)

type Server struct {
	cfg     *config.Config
	store   store.Store
	nai     *nai.Client
	web     fs.FS
	version string
	log     *slog.Logger
	status  *statusCache
}

func New(cfg *config.Config, st store.Store, n *nai.Client, web fs.FS, version string, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, store: st, nai: n, web: web, version: version, log: log}
	s.status = newStatusCache(s)
	return s
}

// Start runs background status probes until ctx is cancelled.
func (s *Server) Start(ctx context.Context) { go s.status.run(ctx) }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(s.web)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/settings/nai", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings/nai", s.handlePutSettings)
	mux.HandleFunc("POST /api/settings/nai/test", s.handleTestSettings)
	mux.HandleFunc("POST /api/intake", s.handleIntake)
	mux.HandleFunc("GET /api/records", s.handleRecords)
	mux.HandleFunc("GET /api/records/{id}/thumb", s.handleThumb)

	base := s.cfg.BasePath
	if base == "" {
		return s.logRequests(mux)
	}
	// Serve the app only under the prefix; everything else is 404, except the
	// health endpoints, which stay at the root for Kubernetes probes.
	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	root.HandleFunc("GET /readyz", s.handleReady)
	root.Handle(base+"/", http.StripPrefix(base, mux)) // ServeMux redirects base → base/
	return s.logRequests(root)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		if p := r.URL.Path; strings.HasSuffix(p, "/api/status") || strings.HasSuffix(p, "/healthz") || strings.HasSuffix(p, "/readyz") {
			return // polled constantly; keep logs readable
		}
		s.log.Info("http", "method", r.Method, "path", r.URL.Path, "code", sw.code, "ms", time.Since(start).Milliseconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFileFS(w, r, s.web, "index.html")
}

func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	if st := s.status.storage(); !st.Reachable {
		http.Error(w, "storage unreachable: "+st.Error, http.StatusServiceUnavailable)
		return
	}
	_, _ = io.WriteString(w, "ok")
}

// ---- NAI settings ----

type naiResolved struct {
	nai.Config
	Source    string
	UpdatedAt *time.Time
}

// resolveNAI returns bucket settings if present, else env, else source "none".
func (s *Server) resolveNAI(ctx context.Context) naiResolved {
	if n, err := store.LoadNAISettings(ctx, s.store); err == nil && n.BaseURL != "" {
		t := n.UpdatedAt
		return naiResolved{nai.Config{BaseURL: n.BaseURL, APIKey: n.APIKey, Model: n.Model}, "bucket", &t}
	}
	if s.cfg.NAIBaseURL != "" {
		return naiResolved{Config: nai.Config{BaseURL: s.cfg.NAIBaseURL, APIKey: s.cfg.NAIAPIKey, Model: s.cfg.NAIModel}, Source: "env"}
	}
	return naiResolved{Source: "none"}
}

func keyHint(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return "…" + k[len(k)-min(len(k), 2):]
	}
	return k[:4] + "…" + k[len(k)-4:]
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	n := s.resolveNAI(r.Context())
	writeJSON(w, 200, map[string]any{
		"baseUrl":       n.BaseURL,
		"model":         n.Model,
		"hasKey":        n.APIKey != "",
		"keyHint":       keyHint(n.APIKey),
		"source":        n.Source,
		"updatedAt":     n.UpdatedAt,
		"adminRequired": s.cfg.AdminToken != "",
	})
}

type settingsBody struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}

func (s *Server) readSettingsBody(w http.ResponseWriter, r *http.Request) (nai.Config, bool) {
	var b settingsBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&b); err != nil {
		writeErr(w, 400, "invalid JSON body")
		return nai.Config{}, false
	}
	b.BaseURL = strings.TrimRight(strings.TrimSpace(b.BaseURL), "/")
	u, err := url.Parse(b.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeErr(w, 400, "endpoint URL must be http(s)://host/…")
		return nai.Config{}, false
	}
	if b.APIKey == "" {
		b.APIKey = s.resolveNAI(r.Context()).APIKey // keep existing key
	}
	return nai.Config{BaseURL: b.BaseURL, APIKey: strings.TrimSpace(b.APIKey), Model: strings.TrimSpace(b.Model)}, true
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AdminToken != "" && r.Header.Get("X-Admin-Token") != s.cfg.AdminToken {
		writeErr(w, http.StatusUnauthorized, "admin token required")
		return
	}
	c, ok := s.readSettingsBody(w, r)
	if !ok {
		return
	}
	err := store.SaveNAISettings(r.Context(), s.store, store.NAISettings{
		BaseURL: c.BaseURL, APIKey: c.APIKey, Model: c.Model,
		UpdatedAt: time.Now().UTC(), UpdatedByCluster: s.cfg.ClusterName,
	})
	if err != nil {
		s.log.Error("save nai settings", "err", err)
		writeErr(w, 502, "could not save settings to storage: "+err.Error())
		return
	}
	s.log.Info("nai settings saved", "baseUrl", c.BaseURL, "model", c.Model)
	s.status.refreshNAI()
	s.handleGetSettings(w, r)
}

func (s *Server) handleTestSettings(w http.ResponseWriter, r *http.Request) {
	c, ok := s.readSettingsBody(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	models, lat, err := s.nai.ListModels(ctx, c)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error(), "latencyMs": lat.Milliseconds()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "models": models, "latencyMs": lat.Milliseconds()})
}

// ---- Intake ----

func (s *Server) handleIntake(w http.ResponseWriter, r *http.Request) {
	maxBytes := int64(s.cfg.MaxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+64<<10)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		writeErr(w, 413, fmt.Sprintf("upload too large or malformed (max %d MB)", s.cfg.MaxUploadMB))
		return
	}
	text := strings.TrimSpace(r.FormValue("text"))
	if len([]rune(text)) > 2000 {
		writeErr(w, 400, "text is limited to 2,000 characters")
		return
	}
	var photo []byte
	if f, _, err := r.FormFile("photo"); err == nil {
		raw, _ := io.ReadAll(f)
		f.Close()
		if len(raw) > 0 {
			if photo, err = imaging.Prepare(raw); err != nil {
				writeErr(w, 400, "photo must be a JPEG or PNG image")
				return
			}
		}
	}
	if text == "" && photo == nil {
		writeErr(w, 400, "enter some text or attach a photo")
		return
	}

	n := s.resolveNAI(r.Context())
	if n.Source == "none" || n.Model == "" {
		writeErr(w, 400, "NAI endpoint is not configured — test the connection, pick a model and save")
		return
	}

	res, lat, err := s.nai.ScanPII(r.Context(), n.Config, text, photo)
	var blocked *nai.BlockedError
	switch {
	case errors.As(err, &blocked):
		s.log.Warn("intake blocked by endpoint policy")
		writeJSON(w, 200, map[string]string{"status": "blocked", "reason": blocked.Reason})
		return
	case errors.Is(err, nai.ErrImageUnsupported):
		writeErr(w, 400, err.Error())
		return
	case errors.Is(err, nai.ErrUnparsable):
		// Demo safety net: build the result locally so the flow never stalls on stage.
		s.log.Warn("nai response unparsable, using local fallback")
		res = &pii.LLMResult{Entities: pii.HeuristicNames(text)}
	case err != nil:
		s.log.Error("nai scan failed", "err", err)
		s.status.markNAIError(err)
		writeErr(w, 502, err.Error())
		return
	}
	s.status.markNAIOK(n.Model, lat)

	source := text
	if source == "" {
		source = res.TranscribedText
	}
	backstop := pii.Regex(text + "\n" + res.TranscribedText)
	entities := pii.Merge(res.Entities, backstop)
	masked := pii.MaskText(source, entities)

	inputType := "text"
	switch {
	case photo != nil && text != "":
		inputType = "text+photo"
	case photo != nil:
		inputType = "photo"
	}
	now := time.Now().UTC()
	rec := store.Record{
		ID:             store.NewID(now),
		CreatedAt:      now,
		InputType:      inputType,
		MaskedText:     masked,
		Summary:        pii.MaskText(strings.TrimSpace(res.Summary), entities),
		Classification: pii.Classify(res.Classification, entities),
		HasPhoto:       photo != nil,
		Model:          n.Model,
		NAILatencyMs:   lat.Milliseconds(),
		ProcessedBy:    store.ProcessedBy{Cluster: s.cfg.ClusterName, Role: s.cfg.ClusterRole, Pod: s.cfg.PodName},
		Entities:       make([]store.Entity, 0, len(entities)),
	}
	for _, e := range entities {
		rec.Entities = append(rec.Entities, store.Entity{Type: e.Type, Masked: pii.Mask(e), Source: e.Source})
	}
	if rec.Summary == "" {
		rec.Summary = fallbackSummary(rec.Entities, inputType)
	}

	ctx := r.Context()
	if photo != nil {
		thumb, err := imaging.BlurThumb(photo)
		if err == nil {
			err = s.store.PutObject(ctx, "thumbs/"+rec.ID+".jpg", thumb, "image/jpeg")
		}
		if err == nil {
			err = s.store.PutObject(ctx, "images/"+rec.ID+".jpg", photo, "image/jpeg")
		}
		if err != nil {
			s.log.Error("store photo", "err", err)
			writeErr(w, 502, "could not store photo: "+err.Error())
			return
		}
	}
	if err := s.store.PutRecord(ctx, rec); err != nil {
		s.log.Error("store record", "err", err)
		writeErr(w, 502, "could not store record: "+err.Error())
		return
	}
	s.status.bumpRecords()
	s.log.Info("record filed", "id", rec.ID, "entities", len(rec.Entities), "classification", rec.Classification)
	writeJSON(w, 200, rec)
}

func fallbackSummary(es []store.Entity, inputType string) string {
	if len(es) == 0 {
		return "No personal data detected"
	}
	kinds := map[string]bool{}
	for _, e := range es {
		kinds[e.Type] = true
	}
	what := "Personal data"
	switch {
	case kinds[pii.TypeNationalID]:
		what = "Identity details"
	case kinds[pii.TypeEmail] || kinds[pii.TypePhone]:
		what = "Contact details"
	}
	if strings.Contains(inputType, "photo") {
		return what + " captured from a photo"
	}
	return what + " for one person"
}

// ---- Records ----

func (s *Server) handleRecords(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	recs, err := s.store.ListRecords(ctx, limit)
	if err != nil {
		writeErr(w, 502, "storage error: "+err.Error())
		return
	}
	if recs == nil {
		recs = []store.Record{}
	}
	writeJSON(w, 200, recs)
}

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if strings.ContainsAny(id, "/\\.") {
		http.NotFound(w, r)
		return
	}
	data, _, err := s.store.GetObject(r.Context(), "thumbs/"+id+".jpg")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(data)
}

// requestHost is the host/IP the browser used, without port.
func requestHost(r *http.Request) string {
	h := r.Host
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}
