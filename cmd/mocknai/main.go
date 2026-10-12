// Command mocknai is a tiny OpenAI-compatible server for local testing and as
// an on-stage fallback if the real NAI endpoint is unavailable.
//
//	GET  /v1/models            → mock-vision, mock-text
//	POST /v1/chat/completions  → deterministic PII JSON built from regex matches
//
// MOCK_FENCED=true wraps answers in ```json fences. Input containing
// "ignore your instructions" returns 403 {"error":"blocked by guardrail"}.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rhapsody008/sovereign-ai-demo/internal/pii"
)

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":9090"
	}
	fenced := os.Getenv("MOCK_FENCED") == "true"

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"object": "list", "data": []map[string]string{
			{"id": "mock-vision", "object": "model"}, {"id": "mock-text", "object": "model"},
		}})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad request"})
			return
		}
		var text string
		hasImage := false
		for _, m := range req.Messages {
			if m.Role != "user" {
				continue
			}
			var s string
			if json.Unmarshal(m.Content, &s) == nil {
				text = s
				continue
			}
			var parts []map[string]any
			if json.Unmarshal(m.Content, &parts) == nil {
				for _, p := range parts {
					if p["type"] == "text" {
						text, _ = p["text"].(string)
					}
					if p["type"] == "image_url" {
						hasImage = true
					}
				}
			}
		}
		if strings.Contains(strings.ToLower(text), "ignore your instructions") {
			writeJSON(w, 403, map[string]string{"error": "blocked by guardrail"})
			return
		}
		if hasImage && req.Model == "mock-text" {
			writeJSON(w, 400, map[string]string{"error": "model does not support image input"})
			return
		}
		time.Sleep(400 * time.Millisecond) // feel like a real model

		entities := append(pii.HeuristicNames(text), pii.Regex(text)...)
		transcribed := ""
		if hasImage {
			transcribed = "Alex Tan | Senior Officer | alex.tan@agency.gov.sg | +65 9876 5432"
			entities = append(entities, pii.HeuristicNames("name: Alex Tan")...)
			entities = append(entities, pii.Regex(transcribed)...)
		}
		class, summary := "Internal", "General information with no personal data"
		if len(entities) > 0 {
			class, summary = "Confidential", "Message containing contact details for a person"
		}
		for _, e := range entities {
			if e.Type == pii.TypeNationalID {
				class, summary = "Restricted", "Message containing identity details for a person"
			}
		}
		if hasImage {
			summary = "Business card of a government officer"
		}
		out, _ := json.Marshal(pii.LLMResult{Entities: entities, TranscribedText: transcribed, Summary: summary, Classification: class})
		content := string(out)
		if fenced {
			content = "```json\n" + content + "\n```"
		}
		writeJSON(w, 200, map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion", "model": req.Model,
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
				"message": map[string]string{"role": "assistant", "content": content}}},
		})
	})
	log.Printf("mocknai listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
