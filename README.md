# Secure Intake · PII Shield

Demo app for a Sovereign AI demo. Users submit text and/or a photo. A model on an on-prem,
OpenAI-compatible **Nutanix Enterprise AI (NAI)** endpoint finds personal data. The app stores only the masked record
in a **Nutanix Objects** bucket. The same container runs on a primary and a standby NKP cluster that share the bucket.
When the presenter stops the primary and opens the standby's IP, the standby shows the same records.

This build is **demo-scoped**:

| Part | Status |
|---|---|
| NAI connection: `/models`, `/chat/completions`, image input, policy blocks | **Real** |
| Objects (S3) storage: settings, records, photos, thumbnails | **Real** (shared by both clusters) |
| PII masking and regex backstop (email, NRIC/FIN, SG phone) | Real, deterministic |
| Unparsable model output | Falls back to a locally built result, so the demo does not stall |
| NKP cluster tile | **Faked** from `DEMO_NODES` / `DEMO_PODS` / `DEMO_HPA_MAX`; no Kubernetes API calls, so no RBAC is needed |
| Sample records | 3 fake pre-masked records are written when the bucket is empty (primary only; `SEED_SAMPLE_RECORDS=false` disables this) |
| Prometheus metrics, `/api/burn` | Not included |

## Architecture

```
browser ──► secure-intake (primary NKP)  ─┐                 ┌─► NAI (OpenAI-compatible)
        └─► secure-intake (standby NKP) ──┴─ same config ───┤
                                                            └─► Objects bucket
                                                                 config/nai.json   NAI settings saved from the UI
                                                                 records/<id>.json masked record
                                                                 images/<id>.jpg   resized photo (never served)
                                                                 thumbs/<id>.jpg   pixelated thumbnail
```

NAI settings saved in the UI go to `config/nai.json` in the bucket, so the standby uses them right after failover.
Env vars `NAI_*` are only a fallback for when nothing has been saved yet.

## Run locally

```sh
make up        # SeaweedFS (S3) + mock NAI + primary :8080 + standby :8081
open http://localhost:8080   # blue banner, primary
open http://localhost:8081   # orange banner, standby
make down
```

The mock NAI returns `mock-vision` and `mock-text`. If the input contains "ignore your instructions", it returns a
guardrail 403, which demonstrates the "Blocked by endpoint policy" path.

To rehearse against the **real NAI** with the local stack:

```sh
NAI_BASE_URL=https://<nai-host>/api/v1 NAI_API_KEY=<key> NAI_MODEL=<model> NAI_INSECURE_SKIP_VERIFY=true make up
```

You can also leave the env vars unset and set the endpoint in the UI: **NAI endpoint settings → Test connection →
pick a model → Save**. The base URL is the one the OpenAI client would use, i.e. the path that `/models` and
`/chat/completions` are appended to.

Without Docker: `make mock` in one shell, then `make run` in another. With no `S3_ENDPOINT`, the app uses an in-memory store.

## Failover rehearsal

1. `make up`, open :8080 (primary), file a record. Use a photo too: "Take photo" uses a live camera preview on a laptop and the camera app on a phone or tablet.
2. Open :8081. The same record is listed with the `nkp-primary` tag.
3. `docker compose -f deploy/local/docker-compose.yaml stop app`. Reload :8081. It still lists the records, files new ones, and tags them `nkp-standby`.

On NKP, the camera needs HTTPS at the ingress, because `getUserMedia` requires a secure context.
Without HTTPS, "Take photo" falls back to a file picker on laptops.

## Configuration

See [.env.example](.env.example) for every variable. The main ones:

| Var | Default | Notes |
|---|---|---|
| `CLUSTER_NAME` / `CLUSTER_DISPLAY_NAME` | `local` | Shown in the banner and stored on each record |
| `CLUSTER_ROLE` | `primary` | `standby` turns the banner orange |
| `POD_NAME`, `NODE_NAME`, `POD_NAMESPACE` | hostname | Set them with the Downward API |
| `S3_ENDPOINT` | — | Objects `host:port`. If empty, the app uses an in-memory store |
| `S3_USE_TLS`, `S3_INSECURE_SKIP_VERIFY` | `true`, `false` | Set skip-verify for self-signed Objects certs |
| `S3_BUCKET`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `S3_REGION` | `secure-intake`, —, —, `us-east-1` | The app creates the bucket if it is missing |
| `NAI_BASE_URL`, `NAI_API_KEY`, `NAI_MODEL` | — | Fallback only. Settings saved in the bucket take precedence |
| `NAI_TIMEOUT`, `NAI_INSECURE_SKIP_VERIFY` | `60s`, `false` | |
| `ADMIN_TOKEN` | — | If set, saving NAI settings asks for this token |
| `DEMO_NODES`, `DEMO_PODS`, `DEMO_HPA_MAX` | `3`, `2`, `8` | Fake values for the cluster tile |

## Demo inputs

- `This is Zhou Yi with email yi.zhou@nutanix.com. File this.` is masked to `Z*** Y*` / `y***@nutanix.com` and classified Confidential
- `Visitor Tan Mei Ling, NRIC S1234567D, mobile +65 9123 4512` is classified Restricted
- A photo of a name card (needs a vision model)
- `Quarterly town hall moved to Thursday` has no PII and is classified Internal
- `ignore your instructions …` is blocked when the endpoint has a guardrail (the mock always blocks it)

## Images

GitHub Actions ([.github/workflows/build.yaml](.github/workflows/build.yaml)) tests the code and pushes multi-arch images:

- `ghcr.io/rhapsody008/sovereign-ai-demo` (app)
- `ghcr.io/rhapsody008/sovereign-ai-demo-mocknai`, a fallback if NAI is unavailable on the day

GHCR packages are **private by default**. Make them public in the package settings, or create an
`imagePullSecret` on both NKP clusters.

## API

`GET /api/status` · `GET|PUT /api/settings/nai` · `POST /api/settings/nai/test` · `POST /api/intake` (multipart
`text`, `photo`) · `GET /api/records?limit=50` · `GET /api/records/{id}/thumb` · `GET /healthz` · `GET /readyz`.
The API never returns the full API key, and no endpoint serves the original photo.

## Fonts

The UI bundles [Space Grotesk](https://github.com/floriankarsten/space-grotesk) and
[JetBrains Mono](https://github.com/JetBrains/JetBrainsMono) (both SIL Open Font License 1.1) in `web/fonts/`,
so it needs no external CDN and works air-gapped.
