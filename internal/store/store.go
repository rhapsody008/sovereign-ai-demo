// Package store persists records, images and the shared NAI settings in an
// S3-compatible bucket (Nutanix Objects / MinIO), with an in-memory fallback.
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("not found")

type Entity struct {
	Type   string `json:"type"`
	Masked string `json:"masked"`
	Source string `json:"source"`
}

type ProcessedBy struct {
	Cluster string `json:"cluster"`
	Role    string `json:"role"`
	Pod     string `json:"pod"`
}

type Record struct {
	ID             string      `json:"id"`
	CreatedAt      time.Time   `json:"createdAt"`
	InputType      string      `json:"inputType"`
	MaskedText     string      `json:"maskedText"`
	Summary        string      `json:"summary"`
	Classification string      `json:"classification"`
	Entities       []Entity    `json:"entities"`
	HasPhoto       bool        `json:"hasPhoto"`
	Model          string      `json:"model"`
	NAILatencyMs   int64       `json:"naiLatencyMs"`
	ProcessedBy    ProcessedBy `json:"processedBy"`
}

// NAISettings is stored at config/nai.json so both clusters share it.
type NAISettings struct {
	BaseURL          string    `json:"baseUrl"`
	APIKey           string    `json:"apiKey"`
	Model            string    `json:"model"`
	UpdatedAt        time.Time `json:"updatedAt"`
	UpdatedByCluster string    `json:"updatedByCluster"`
}

type Store interface {
	PutRecord(ctx context.Context, r Record) error
	ListRecords(ctx context.Context, limit int) ([]Record, error)
	PutObject(ctx context.Context, key string, data []byte, contentType string) error
	GetObject(ctx context.Context, key string) ([]byte, string, error)
	CountRecords(ctx context.Context) (int, error)
	Ping(ctx context.Context) error
	Backend() string
}

// NewID returns a time-sortable record ID, e.g. 20261012T093015Z-a1b2c3.
func NewID(t time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return t.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b)
}

const settingsKey = "config/nai.json"

func LoadNAISettings(ctx context.Context, s Store) (*NAISettings, error) {
	data, _, err := s.GetObject(ctx, settingsKey)
	if err != nil {
		return nil, err
	}
	var n NAISettings
	if err := json.Unmarshal(data, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

func SaveNAISettings(ctx context.Context, s Store, n NAISettings) error {
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return s.PutObject(ctx, settingsKey, data, "application/json")
}

// Memory is an in-process Store for local runs without a bucket.
type Memory struct {
	mu      sync.RWMutex
	objects map[string]memObj
}

type memObj struct {
	data []byte
	ct   string
}

func NewMemory() *Memory { return &Memory{objects: map[string]memObj{}} }

func (m *Memory) Backend() string { return "memory" }

func (m *Memory) PutRecord(ctx context.Context, r Record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return m.PutObject(ctx, "records/"+r.ID+".json", data, "application/json")
}

func (m *Memory) ListRecords(_ context.Context, limit int) ([]Record, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys []string
	for k := range m.objects {
		if strings.HasPrefix(k, "records/") {
			keys = append(keys, k)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	var out []Record
	for _, k := range keys {
		if len(out) >= limit {
			break
		}
		var r Record
		if json.Unmarshal(m.objects[k].data, &r) == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *Memory) PutObject(_ context.Context, key string, data []byte, ct string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = memObj{append([]byte(nil), data...), ct}
	return nil
}

func (m *Memory) GetObject(_ context.Context, key string) ([]byte, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	o, ok := m.objects[key]
	if !ok {
		return nil, "", ErrNotFound
	}
	return o.data, o.ct, nil
}

func (m *Memory) CountRecords(_ context.Context) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for k := range m.objects {
		if strings.HasPrefix(k, "records/") {
			n++
		}
	}
	return n, nil
}

func (m *Memory) Ping(context.Context) error { return nil }
