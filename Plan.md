# Secure Intake (PII Shield) — Build Plan for Claude Code

## 0. How to work through this plan

- Work **phase by phase**. At the end of each phase: run `go vet ./...` and `go test ./...`, make sure the acceptance criteria pass, then commit with a message like `phase N: <summary>`. Stop and summarise what was done before starting the next phase.
- Use the **latest stable Go** available (check `go version`; target Go 1.25+). Pin the same version in `go.mod`, the Dockerfile and the GitHub workflow.
- Prefer the standard library. Allowed third-party modules: `github.com/minio/minio-go/v7`, `k8s.io/client-go` (+ `k8s.io/api`, `k8s.io/apimachinery`), `github.com/prometheus/client_golang`, `golang.org/x/image`. Ask before adding anything else.
- No frontend build step. The UI is plain HTML/CSS/JS embedded with `go:embed`.
- Never log API keys, raw intake text, or unmasked PII values.

## 1. Context

A demo app for a 15-minute **Sovereign AI** demo at a government exhibition. The same container runs on two NKP (Kubernetes) clusters — primary and standby — sharing one S3-compatible bucket (Nutanix Objects). The presenter shuts down the primary, opens the secondary's IP, and the app shows the same records, now served by a different cluster.

**What the app does:** the user submits a short text and/or a photo (name card, badge, form). An LLM on an on-prem, OpenAI-compatible endpoint (Nutanix Enterprise AI, "NAI") detects personal data (PII), returns masked text and a classification. The app stores only the masked record (plus the photo, which is only ever served as a blurred thumbnail) in the bucket and lists filed records.

**Screen layout (single page, top to bottom):**
1. **Connection banner** — "You're connected to <cluster display name>", the host/IP the browser used, and the pod name. Banner colour comes from config (blue for primary, orange for standby).
2. **Status row** — three tiles: NKP cluster (health, nodes ready, app pods current/HPA max), NAI endpoint (connected/error, model, latency), Storage (reachable, record count).
3. **NAI endpoint settings** — endpoint URL, API key (password field), model dropdown populated by "Test connection", Save button.
4. **Secure intake** (left) — textarea, "Attach photo" (camera), "Scan and file" button, result panel: PII chips, masked text, classification chip, summary.
5. **Filed records** (right) — newest first, masked summary line, blurred thumbnail if a photo exists, classification, and the cluster that processed it.

## 2. Repository layout

```
secure-intake/
├── cmd/
│   ├── server/main.go          # app entrypoint
│   └── mocknai/main.go         # mock OpenAI-compatible server for local testing
├── internal/
│   ├── config/                 # env parsing, defaults, validation
│   ├── server/                 # HTTP handlers, routing, middleware
│   ├── nai/                    # OpenAI-compatible client (models, chat completions)
│   ├── pii/                    # regex detector, masking, merge, LLM response parsing
│   ├── store/                  # Store interface + minio impl + in-memory impl
│   ├── imaging/                # resize, JPEG encode, blurred thumbnail
│   ├── k8sstatus/              # in-cluster status collector with cache; no-op when local
│   └── metrics/                # Prometheus collectors
├── web/                        # index.html, app.css, app.js (embedded)
├── deploy/local/
│   └── docker-compose.yaml     # app + MinIO + bucket init + mocknai
├── Dockerfile
├── Makefile
├── .github/workflows/build.yaml
├── .env.example
└── README.md
```

Key interfaces (define early, use everywhere so tests can use fakes):

```go
type Store interface {
    PutRecord(ctx, Record) error
    ListRecords(ctx, limit int) ([]Record, error)
    PutObject(ctx, key string, data []byte, contentType string) error
    GetObject(ctx, key string) ([]byte, string, error)
    CountRecords(ctx) (int, error)
    Ping(ctx) error
}

type LLM interface {
    ListModels(ctx, cfg NAIConfig) ([]string, time.Duration, error)
    ScanPII(ctx, cfg NAIConfig, text string, image []byte) (ScanResult, time.Duration, error)
}
```

## 3. Configuration (environment variables)

| Var | Default | Purpose |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `CLUSTER_NAME` | `local` | Cluster identifier shown in banner and stored on records |
| `CLUSTER_DISPLAY_NAME` | value of `CLUSTER_NAME` | Friendly name for banner |
| `CLUSTER_ROLE` | `primary` | `primary` or `standby` |
| `BANNER_COLOR` | `blue` for primary, `orange` for standby | Banner colour |
| `POD_NAME`, `NODE_NAME`, `POD_NAMESPACE` | hostname / empty | Set via Downward API in k8s |
| `HPA_NAME` | `secure-intake` | HPA to read for current/max replicas |
| `APP_LABEL_SELECTOR` | `app=secure-intake` | Selector for counting app pods |
| `S3_ENDPOINT` | — (required) | Host:port of Objects / MinIO |
| `S3_USE_TLS` | `true` | |
| `S3_INSECURE_SKIP_VERIFY` | `false` | For self-signed Objects certs in the lab |
| `S3_BUCKET` | `secure-intake` | |
| `S3_ACCESS_KEY`, `S3_SECRET_KEY` | — (required) | |
| `S3_REGION` | `us-east-1` | |
| `NAI_BASE_URL`, `NAI_API_KEY`, `NAI_MODEL` | empty | Fallback NAI config if none saved in bucket |
| `NAI_TIMEOUT` | `60s` | |
| `NAI_INSECURE_SKIP_VERIFY` | `false` | For lab endpoints with self-signed certs |
| `ADMIN_TOKEN` | empty | If set, `PUT /api/settings/nai` requires header `X-Admin-Token` |
| `ENABLE_LOAD_ENDPOINT` | `false` | Enables `/api/burn` for autoscaling demo |
| `MAX_UPLOAD_MB` | `8` | |

**NAI config precedence:** `config/nai.json` in the bucket (saved via UI) overrides env vars. If neither exists, the NAI tile shows "Not configured". Reason: the config must be shared by both clusters so the standby works immediately after failover.

## 4. Data model (bucket layout)

```
config/nai.json                 # {"baseUrl","apiKey","model","updatedAt","updatedByCluster"}
records/<id>.json               # masked record (see below)
images/<id>.jpg                 # resized photo (max 1024px), never served directly
thumbs/<id>.jpg                 # heavily blurred/pixelated thumbnail, served to UI
```

`<id>` = time-sortable ID (e.g. `20261012T093015Z-<6 random hex>`) so listing by key gives chronological order.

```json
{
  "id": "20261012T093015Z-a1b2c3",
  "createdAt": "2026-10-12T09:30:15Z",
  "inputType": "text | photo | text+photo",
  "maskedText": "This is Z*** Y* with email y***@nutanix.com.",
  "summary": "Contact details for one person",
  "classification": "Public | Internal | Confidential | Restricted",
  "entities": [ {"type": "PERSON_NAME", "masked": "Z*** Y*", "source": "llm|regex"} ],
  "hasPhoto": true,
  "model": "llama-3.2-11b-vision",
  "naiLatencyMs": 640,
  "processedBy": {"cluster": "nkp-sg-primary", "role": "primary", "pod": "secure-intake-7f9c-x2kq"}
}
```

**Never store** raw input text or raw entity values in any record. Entity `value` is used in memory only to perform masking.

## 5. PII scanning logic (`internal/pii`, `internal/nai`)

1. **Input**: text (optional, max 2,000 chars) and/or photo (optional). At least one required.
2. **Image prep**: decode JPEG/PNG, downscale to max 1024px longest side, re-encode JPEG q=85. Strip metadata as a side effect of re-encoding.
3. **LLM call**: `POST {baseUrl}/chat/completions`, OpenAI format, `temperature: 0`. Photo sent as `image_url` content part with a `data:image/jpeg;base64,...` URL. If the model rejects image input, return a clear error ("This model doesn't accept images. Choose a vision model or send text only.").
4. **System prompt** (store as a Go const, easy to tweak):
   > You are a data-protection filter. Find all personal data in the user's text and image: person names, email addresses, phone numbers, national ID numbers (e.g. Singapore NRIC/FIN), postal addresses, dates of birth, and any other personal identifiers. Respond with ONLY a JSON object, no markdown: {"entities":[{"type":"PERSON_NAME|EMAIL|PHONE|NATIONAL_ID|ADDRESS|DOB|OTHER","value":"exact text as it appears"}],"transcribedText":"text visible in the image, if any","summary":"one short sentence describing the content without any personal data","classification":"Public|Internal|Confidential|Restricted"}. Use Confidential if any personal data is present, Restricted if a national ID number is present.
5. **Robust parsing**: strip ```json fences, extract the first balanced `{...}`, unmarshal. On parse failure, retry once with a short "Return only valid JSON" follow-up; if still failing, fall back to regex-only results and mark `summary` as "AI response could not be parsed".
6. **Deterministic backstop** (always runs, on input text and on `transcribedText`): regex detectors for email, Singapore NRIC/FIN (`[STFGM]\d{7}[A-Z]`), Singapore phone (`(\+65\s?)?[689]\d{3}\s?\d{4}`). Merge with LLM entities (dedupe by value). If regex finds something the LLM missed, `source` = `regex`. Raise classification to at least Confidential if any entity exists, Restricted if `NATIONAL_ID`.
7. **Masking rules**:
   - Name: each word → first letter + `***` for first word, first letter + `*` for subsequent (`Zhou Yi` → `Z*** Y*`)
   - Email: first char of local part + `***@` + domain (`y***@nutanix.com`)
   - NRIC/FIN: first char + `****` + last 4 (`S****567D`)
   - Phone: keep country code and last 2 digits (`+65 9*** **12`)
   - Address / DOB / other: `[ADDRESS]`, `[DOB]`, `[REDACTED]`
   - Replace longest values first, case-sensitive exact match, then case-insensitive pass.
8. **Masked text** = masked input text; if input was photo-only, masked `transcribedText`.
9. **Endpoint policy blocks**: if NAI returns HTTP 400/403 with a body mentioning a guardrail/policy (or any 403), return `{"status":"blocked","reason":"<short reason>"}` and show "Blocked by endpoint policy" in the UI. Do not file a record.

## 6. HTTP API

All JSON responses set `Cache-Control: no-store`.

| Method & path | Behaviour |
|---|---|
| `GET /` | Embedded UI |
| `GET /static/*` | Embedded CSS/JS |
| `GET /healthz` | `200 ok`. **Must not** check NAI or storage. |
| `GET /readyz` | 200 if storage `Ping` succeeds (cached 5s), else 503 |
| `GET /metrics` | Prometheus |
| `GET /api/status` | See schema below. Cache k8s/NAI/storage probes for 5s; never block > 2s total (use per-probe timeouts and return last known value). |
| `GET /api/settings/nai` | `{"baseUrl","model","hasKey":true,"keyHint":"sk-d…1a2b","source":"bucket|env|none","updatedAt"}` — never return the full key |
| `PUT /api/settings/nai` | Body `{"baseUrl","apiKey","model"}`; empty `apiKey` keeps the existing key. Validate URL. Requires `X-Admin-Token` if `ADMIN_TOKEN` is set. |
| `POST /api/settings/nai/test` | Body same as PUT (key optional → use saved). Calls `GET {baseUrl}/models`. Returns `{"ok":true,"models":[...],"latencyMs":123}` or `{"ok":false,"error":"..."}` |
| `POST /api/intake` | `multipart/form-data`: `text` (optional), `photo` (optional file). Returns the saved record, or `{"status":"blocked",...}`, or a 4xx/5xx error JSON `{"error":"..."}` |
| `GET /api/records?limit=50` | Newest first |
| `GET /api/records/{id}/thumb` | Blurred thumbnail JPEG. There is **no** endpoint that serves the original image. |
| `POST /api/burn?ms=200` | Only if `ENABLE_LOAD_ENDPOINT=true`: busy-loop CPU for `ms` (max 1000), for HPA demo |

`/api/status` response:

```json
{
  "connection": {"host": "10.10.20.50", "cluster": "nkp-sg-primary", "displayName": "Primary cluster", "role": "primary", "bannerColor": "blue", "pod": "secure-intake-7f9c-x2kq", "node": "worker-2"},
  "cluster":   {"mode": "in-cluster|local", "healthy": true, "nodesReady": 3, "nodesTotal": 3, "podsReady": 4, "hpaCurrent": 4, "hpaMax": 8, "error": ""},
  "nai":       {"configured": true, "connected": true, "model": "llama-3.2-11b-vision", "latencyMs": 640, "error": ""},
  "storage":   {"reachable": true, "backend": "objects", "recordCount": 12, "error": ""},
  "version":   "v0.1.0+abc1234"
}
```

- `connection.host` = the request's `Host` header (strip port) — this is the IP/hostname the presenter typed, so the switch from primary IP to standby IP is visible.
- `cluster.healthy` = all nodes Ready and ≥1 app pod Ready.
- Local mode (no `KUBERNETES_SERVICE_HOST`): `mode: "local"`, counts omitted, tile shows "Running locally".
- k8s reads needed (document in README for later manifests): `get/list nodes` (cluster-scoped), `list pods` and `get horizontalpodautoscalers` (own namespace). Missing permission → tile shows the error text, app keeps working.

## 7. UI behaviour (`web/`)

- Large, projector-friendly type; works at 1280×720 and on a tablet. Light theme, no external fonts/CDNs (must work air-gapped).
- Banner background from `bannerColor` (`blue` → #1F5FA8-ish, `orange` → #D9822B-ish, white text). Title: "Secure Intake · PII Shield", subtitle "AI runs on our own NAI endpoint. No data leaves this environment."
- Poll `/api/status` every 3s; update banner and tiles without page reload. Dot colours: green ok, amber degraded, red error. If a poll fails, show "Connection lost" state on the banner.
- NAI settings: "Test connection" fills the model dropdown and shows "N models found · X ms"; "Save" persists; show `source` (bucket/env). If `ADMIN_TOKEN` is required, prompt once and keep it in a JS variable (not storage).
- Attach photo: open a camera preview using `getUserMedia` (requires HTTPS or `localhost`); capture to canvas → JPEG blob. If camera is unavailable, fall back to `<input type="file" accept="image/*" capture="environment">`. Show a small preview with a remove button.
- "Scan and file": disable the button while running, show a spinner with "Scanning on NAI…". Render result: PII chips (red tint), masked text (monospace), classification chip (amber for Confidential, red for Restricted, grey otherwise), summary, latency.
- Records list: refresh after each intake and every 10s. Each row: masked summary line (first entity masks joined with " · " or summary), classification chip, small blurred thumbnail if `hasPhoto`, and a cluster tag (`processedBy.cluster`) coloured by role.
- Escape all user-derived strings when rendering (use `textContent`, never `innerHTML` with data).

## 8. Images (`internal/imaging`)

- `Prepare(raw []byte) (jpeg []byte, err)`: decode, downscale to ≤1024px, encode q=85.
- `BlurThumb(raw []byte) (jpeg []byte, err)`: downscale to 16px wide, then upscale to 240px wide with nearest-neighbour (pixelated look), encode q=70. Uses `golang.org/x/image/draw`, no CGO.

## 9. Metrics

- `secure_intake_http_requests_total{path,code}`
- `secure_intake_intake_total{result="filed|blocked|error"}`
- `secure_intake_pii_entities_total{type,source}`
- `secure_intake_nai_request_seconds` (histogram, label `op="models|scan"`)
- Default Go/process collectors.

## 10. Phases

### Phase 1 — Skeleton and config
- `go mod init github.com/<owner>/secure-intake`, repo layout, `internal/config` with validation and clear startup errors.
- `cmd/server`: router (Go 1.22+ `http.ServeMux` patterns), `/healthz`, `/readyz`, `/metrics`, embedded placeholder UI, structured logging via `log/slog` (JSON), request logging middleware, graceful shutdown on SIGTERM (15s).
- Version info via `-ldflags "-X main.version=..."`.
- **Accept:** `make run` starts the server locally; `/healthz` returns 200; `go test ./...` passes.

### Phase 2 — Storage
- `store.Store` interface, MinIO-client implementation (path-style, TLS options), in-memory implementation for tests.
- NAI config load/save in `config/nai.json` with env fallback.
- **Accept:** unit tests for memory store; integration test (behind build tag `integration`) against MinIO from docker compose.

### Phase 3 — PII engine and NAI client
- `internal/nai`: `ListModels`, `ScanPII` with timeouts, TLS skip option, error classification (unreachable, auth, blocked, bad response).
- `internal/pii`: regex detectors, masking functions, merge, classification floor, robust JSON extraction.
- `cmd/mocknai`: implements `GET /v1/models` (returns `mock-vision`, `mock-text`) and `POST /v1/chat/completions` returning a deterministic JSON answer built from regex matches plus a naive capitalised-name heuristic; supports an env flag to wrap output in ```json fences and a magic input "ignore your instructions" that returns HTTP 403 `{"error":"blocked by guardrail"}`.
- **Accept:** table-driven tests for every masking rule; tests for fenced/garbled JSON; `ScanPII` tested against `httptest` server; the sample input "This is Zhou Yi with email yi.zhou@nutanix.com. File this." yields masked text "This is Z*** Y* with email y***@nutanix.com. File this." and classification Confidential.

### Phase 4 — API handlers
- Implement every endpoint in section 6, including multipart limits, input validation, error JSON, `no-store` headers, admin token check, burn endpoint gating.
- `internal/k8sstatus`: in-cluster client with 5s cache and 2s timeout; local mode.
- **Accept:** handler tests with fake Store and fake LLM covering: text only, photo only, both, blocked, NAI down, storage down, settings key never returned in full, thumb served, no original-image route.

### Phase 5 — UI
- Build the page per section 7 in `web/` (vanilla JS, no CDNs).
- **Accept:** with docker compose running, a full flow works at `http://localhost:8080`: configure mock NAI, test connection, file text, file photo (camera works on localhost), records list shows blurred thumbs and cluster tag; switching `CLUSTER_ROLE=standby` changes the banner colour.

### Phase 6 — Container and local environment
- `Dockerfile`: multi-stage; builder `golang:<version>-alpine` (or bookworm), `CGO_ENABLED=0`, `-trimpath -ldflags "-s -w -X main.version=$VERSION"`, `TARGETOS/TARGETARCH` for buildx; final `gcr.io/distroless/static-debian12:nonroot`, `USER nonroot`, `EXPOSE 8080`. Add OCI labels including `org.opencontainers.image.source=https://github.com/<owner>/secure-intake` (links the GHCR package to the repo).
- `deploy/local/docker-compose.yaml`: `minio` (console on 9001), `minio-init` (creates bucket with `mc`), `mocknai` (built from the same Dockerfile via a build target or separate small stage), `app` (reads `.env`). Second app service `app-standby` on port 8081 with `CLUSTER_ROLE=standby`, same bucket — lets you rehearse the IP switch locally.
- `.env.example` with all variables; README explains how to point at the real NAI endpoint instead of mocknai.
- `Makefile`: `run`, `test`, `test-integration`, `lint` (`go vet` + `gofmt -l`), `docker-build`, `up`, `down`, `logs`.
- **Accept:** `make up` → primary at :8080, standby at :8081; a record filed on :8080 appears on :8081 with `processedBy` = primary; stopping the `app` container leaves :8081 fully working.

### Phase 7 — GitHub Actions → GHCR
`.github/workflows/build.yaml`:
- Triggers: `push` to `main`, tags `v*`, and `pull_request` (PRs build only, no push).
- `permissions: contents: read, packages: write`.
- Job `test`: checkout, `actions/setup-go` (version from `go.mod`, cache on), `go vet ./...`, `gofmt` check, `go test -race ./...`.
- Job `image` (needs `test`): `docker/setup-qemu-action`, `docker/setup-buildx-action`, `docker/login-action` to `ghcr.io` with `${{ github.actor }}` / `${{ secrets.GITHUB_TOKEN }}` (skip on PR), `docker/metadata-action` with image `ghcr.io/${{ github.repository }}` (lowercase) and tags: `sha-<short>`, `latest` on default branch, semver on `v*` tags; `docker/build-push-action` with `platforms: linux/amd64,linux/arm64`, `push: ${{ github.event_name != 'pull_request' }}`, `build-args: VERSION=...`, GHA cache (`cache-from/to: type=gha`).
- Also build and push the `mocknai` image (`ghcr.io/<owner>/secure-intake-mocknai`) — handy as a fallback during the live demo if NAI is unavailable.
- **Accept:** workflow YAML passes `actionlint` if available; README documents that the GHCR package is private by default (make it public in package settings, or create an `imagePullSecret` on both NKP clusters).

### Phase 8 — Polish
- README: architecture summary, env var table, local run, rehearsal steps (primary :8080 → stop → standby :8081), required k8s RBAC, demo inputs to use.
- Seed script `make seed` that files 3 sample records via the API (text only) so the list is never empty on stage.
- **Accept:** fresh clone → `make up && make seed` gives a working demo in under 2 minutes.

## 11. Out of scope for this plan (next steps)

- Kubernetes manifests, Kustomize overlays per cluster, Flux setup, HPA, Ingress/TLS — to be planned separately. Design decisions already made: per-cluster ConfigMap for `CLUSTER_*` / `BANNER_COLOR`, Downward API for `POD_NAME`/`NODE_NAME`/`POD_NAMESPACE`, Secret for S3 keys, HTTPS at the ingress (camera requires a secure context).
- Authentication beyond the optional admin token.