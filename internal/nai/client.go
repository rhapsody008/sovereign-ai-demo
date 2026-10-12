// Package nai is a minimal OpenAI-compatible client for Nutanix Enterprise AI.
package nai

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rhapsody008/sovereign-ai-demo/internal/pii"
)

// SystemPrompt asks the model for a strict JSON PII report.
const SystemPrompt = `You are a data-protection filter. Find all personal data in the user's text and image: person names, email addresses, phone numbers, national ID numbers (e.g. Singapore NRIC/FIN), postal addresses, dates of birth, and any other personal identifiers. Respond with ONLY a JSON object, no markdown: {"entities":[{"type":"PERSON_NAME|EMAIL|PHONE|NATIONAL_ID|ADDRESS|DOB|OTHER","value":"exact text as it appears"}],"transcribedText":"text visible in the image, if any","summary":"one short sentence describing the content without any personal data","classification":"Public|Internal|Confidential|Restricted"}. Use Confidential if any personal data is present, Restricted if a national ID number is present.`

type Config struct {
	BaseURL string
	APIKey  string
	Model   string
}

var (
	ErrUnparsable       = errors.New("AI response could not be parsed")
	ErrImageUnsupported = errors.New("This model doesn't accept images. Choose a vision model or send text only.")
)

// BlockedError means the endpoint's guardrail/policy refused the request.
type BlockedError struct{ Reason string }

func (e *BlockedError) Error() string { return "blocked by endpoint policy: " + e.Reason }

type Client struct {
	http *http.Client
}

func New(timeout time.Duration, insecure bool) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // lab self-signed certs
	}
	return &Client{http: &http.Client{Timeout: timeout, Transport: tr}}
}

func (c *Client) do(ctx context.Context, cfg Config, method, path string, body any) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(cfg.BaseURL, "/")+path, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("NAI endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, resp.StatusCode, errors.New("NAI rejected the API key (401)")
	}
	return data, resp.StatusCode, nil
}

// ListModels calls GET {base}/models.
func (c *Client) ListModels(ctx context.Context, cfg Config) ([]string, time.Duration, error) {
	start := time.Now()
	data, code, err := c.do(ctx, cfg, http.MethodGet, "/models", nil)
	lat := time.Since(start)
	if err != nil {
		return nil, lat, err
	}
	if code != http.StatusOK {
		return nil, lat, fmt.Errorf("NAI returned HTTP %d: %s", code, short(data))
	}
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, lat, fmt.Errorf("unexpected /models response: %w", err)
	}
	models := make([]string, 0, len(r.Data))
	for _, m := range r.Data {
		models = append(models, m.ID)
	}
	sort.Strings(models)
	return models, lat, nil
}

type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// ScanPII sends text and/or a JPEG image to the model and parses its JSON answer.
// On unparsable output it retries once, then returns ErrUnparsable.
func (c *Client) ScanPII(ctx context.Context, cfg Config, text string, image []byte) (*pii.LLMResult, time.Duration, error) {
	var user any = text
	if len(image) > 0 {
		t := text
		if t == "" {
			t = "Scan this image for personal data."
		}
		user = []map[string]any{
			{"type": "text", "text": t},
			{"type": "image_url", "image_url": map[string]string{
				"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(image),
			}},
		}
	}
	msgs := []message{{Role: "system", Content: SystemPrompt}, {Role: "user", Content: user}}

	start := time.Now()
	content, err := c.chat(ctx, cfg, msgs, len(image) > 0)
	if err != nil {
		return nil, time.Since(start), err
	}
	if r, err := pii.ParseLLM(content); err == nil {
		return r, time.Since(start), nil
	}
	msgs = append(msgs,
		message{Role: "assistant", Content: content},
		message{Role: "user", Content: "Return only valid JSON matching the schema. No other text."})
	content, err = c.chat(ctx, cfg, msgs, len(image) > 0)
	lat := time.Since(start)
	if err != nil {
		return nil, lat, err
	}
	r, err := pii.ParseLLM(content)
	if err != nil {
		return nil, lat, ErrUnparsable
	}
	return r, lat, nil
}

func (c *Client) chat(ctx context.Context, cfg Config, msgs []message, hasImage bool) (string, error) {
	body := map[string]any{
		"model":       cfg.Model,
		"messages":    msgs,
		"temperature": 0,
		"max_tokens":  1024,
	}
	data, code, err := c.do(ctx, cfg, http.MethodPost, "/chat/completions", body)
	if err != nil {
		return "", err
	}
	if code == http.StatusForbidden || (code == http.StatusBadRequest && mentionsPolicy(data)) {
		return "", &BlockedError{Reason: short(data)}
	}
	if code != http.StatusOK {
		if hasImage && mentionsImage(data) {
			return "", ErrImageUnsupported
		}
		return "", fmt.Errorf("NAI returned HTTP %d: %s", code, short(data))
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &r); err != nil || len(r.Choices) == 0 {
		return "", ErrUnparsable
	}
	return r.Choices[0].Message.Content, nil
}

func mentionsPolicy(b []byte) bool {
	s := strings.ToLower(string(b))
	return strings.Contains(s, "guardrail") || strings.Contains(s, "policy")
}

func mentionsImage(b []byte) bool {
	s := strings.ToLower(string(b))
	return strings.Contains(s, "image") || strings.Contains(s, "multimodal") || strings.Contains(s, "vision")
}

// short extracts a readable error message from a response body.
func short(b []byte) string {
	var e struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal(b, &e) == nil {
		switch v := e.Error.(type) {
		case string:
			return v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				return m
			}
		}
		if e.Message != "" {
			return e.Message
		}
		if e.Detail != "" {
			return e.Detail
		}
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
