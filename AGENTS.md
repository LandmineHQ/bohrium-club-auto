# AGENTS.md

Repository guidance for automated coding agents and human contributors working on `bohrium-club-auto`.

## 1. Project Overview

`bohrium-club-auto` is a self-hosted **Go 1.24** service (`CGO_ENABLED=0` static binary using pure-Go SQLite `modernc.org/sqlite`) providing:

- Multi-account automated & manual daily task execution for **Bohrium User Community (`https://club.bohrium.com/`)** with per-account randomized time-slot check-ins and anti-burst spacing.
- Multi-account remote server-side authentication (WeChat Official Account QR scan, OAuth, OTP, `brmToken` import, automatic per-account JWT refresh), with strict per-account persistent `storeId` isolation.
- Explicit opt-in OpenAI-compatible `/v1/*` gateway: newly logged-in accounts default to `poolEnabled = false` and never enter routing until manually dragged into the routing pool; new sessions sample healthy pooled accounts with probability proportional to effective balance (`max(0, photon + org)`), bind `session_hash -> account_id` in SQLite (`data/bohrium.db`) for a hard max of 7 days, and transparently fail over + rebind pre-first-byte on 401/402/pool-removal.
- An embedded, admin-password-protected Web Dashboard (`web/` built with React + Vite + TypeScript + Tailwind CSS + shadcn/ui into `web/dist` and embedded via `go:embed all:dist`) managing multiple accounts, drag-and-drop OpenAI routing pool orchestration, 7-day SQLite session bindings, remaining photons, expiring lots, ledger history, membership quotas, knowledge-base storage capacity, daily tasks, execution logs, schedule settings, and live frontend recon drift checks.
- A built-in frontend snapshot/diff checker (`recon pull` / `recon diff`) writing local baselines to gitignored `/recon/`.

## 2. Non-Negotiable Invariants

Follow these rules strictly on every change:

1. **Chrome Browser Outgoing Fingerprint**: All outbound HTTP traffic MUST go through `internal/chromehttp` with a realistic Desktop Chrome `User-Agent`, `Sec-CH-UA`, `Sec-CH-UA-Mobile`, `Sec-CH-UA-Platform`, and `Accept-Language`. Never leak Go's default `Go-http-client`.
2. **Do Not Call Deprecated Check-In**: Never call or re-add the deprecated legacy 200-photon check-in endpoint (`/bohrapi/v1/account/user/integral_receive`).
3. **Authentic Desktop Client Task (Task #2)**: `bohrium_science_space_client` (`体验玻尔科学空间「客户端」`) MUST execute via Bohrium Desktop Client's native SciX LLM transport (`x-scix-runtime: scix_local`, no Chrome UA / `Sec-CH-UA*`): `GET /models?tag=shrimp-brain-v2` → `POST /billing/preflight` → `POST /scix/llm/chat/completions` carrying the exact `SciMaster` system prompt + tool sections + `<environment_context>`, all 16 embedded desktop `tools` (`internal/prompts/data/desktop_tools.json`, `strict: false`), canonical per-account `x-scix-execution` (`scenario: "local_agent"`), shared W3C `traceparent`, and `stream: true`; immediately cancel the HTTP stream after acceptance (`cancelAfterMs`) and report `POST /api/v1/query-events` with `task_key: "bohrium_science_space_client"` (mirroring `scix.exe` module `76272`/`93206`).
4. **Real Web Task Flow + Immediate Termination**:
   - **Task 1 (`bohrium_science_space_web`)**: Pick a random prompt from `internal/prompts/data/science_space_prompts.json`, post to `/api/v1/chat?historyCapabilities=sessionForkHistoryV1` targeting `{"kind":"team","teamId":"AgK4D5B8Fdju"}`, report `POST /api/v1/query-events`, and immediately cancel via `POST /api/v1/chat/<workflowId>/cancel` (`{"source":"api_request"}`).
   - **Task 2 (`bohrium_science_space_client`)**: Pick a random prompt from `science_space_prompts.json`, run authentic desktop client `/billing/preflight` + streaming `/scix/llm/chat/completions` with full 16 desktop tools (`desktop_tools.json`), immediately cancel the SSE stream after acceptance, and report `POST /api/v1/query-events` (`task_key: "bohrium_science_space_client"`).
   - **Task 3 (`bohrium_literature_reading`)**: Pick a random PDF from `internal/prompts/data/papers_catalog.json` (100 curated arXiv papers, cached in `data/pdfs/`, or user PDFs in `data/custom_pdfs/`), upload through `literature-sage` (`folder/create` → `file/multipart` → `/api/upload/binary` with `X-Storage-Param` → `file/submit`), register at `scix/reader/entries`, submit a random question from `literature_reading_prompts.json` with `readerEntryId`, report `query-events`, and immediately cancel the workflow.
   - **Task 4 (`bohrium_academic_writing`)**: Pick a random prompt from `academic_writing_prompts.json`, submit chat with `Referer: https://www.bohrium.com/academic-writing`, report `query-events`, and immediately cancel the workflow.
5. **No CAPTCHA Bypass**: Password login (`platform.bohrium.com/api/account/v3/login`) is only attempted when `/api/captcha/check_user` explicitly returns `skip: true`. Never implement CAPTCHA solving or bypass logic; guide users to WeChat QR login or `brmToken` import.
6. **Admin Authentication Gate**: Keep all operational `/api/*` endpoints behind `requireAdmin` in `internal/server/server.go`. Only `/healthz`, static SPA assets, and `/api/auth/{status,login,logout}` are publicly reachable.
7. **No Vendored Upstream Recon or Runtime Secrets**: Never commit `/recon/`, `/data/`, tokens, passwords, or downloaded upstream bundles to Git.
8. **Pure-Go Static Builds (`CGO_ENABLED=0`)**: Only pure-Go dependencies (specifically `modernc.org/sqlite` pinned for Go 1.24 compatibility) are permitted so `CGO_ENABLED=0` static cross-compilation across all 5 OS/arch targets and minimal Alpine Docker builds remain fast, reproducible, and self-contained.
9. **Explicit OpenAI Pool Opt-In & 7-Day Binding Cap**: Newly authenticated or migrated accounts MUST default to `poolEnabled = false`. SQLite `session_bindings` MUST strictly enforce a 7-day hard TTL (`created_at + 7d`, physically purged on expiry) and per-account isolated `storeId`.

## 3. Repository Layout

```text
cmd/bohrium-club-auto/main.go     CLI entry point (serve, recon pull, recon diff, version)
internal/chromehttp/              Chrome UA & Client Hints HTTP client wrapper + tests
internal/config/                  Atomic config.json management, env overrides, first-boot password
internal/store/                   Pure-Go SQLite (data/bohrium.db) multi-account, 7d session bindings, responses & logs store + lossless legacy state.json migration
internal/prompts/                 go:embed loader for prompt corpora & 100-paper catalog + tests
  data/science_space_prompts.json     330 scientific prompts (Task 1)
  data/academic_writing_prompts.json  320 academic writing prompts (Task 4)
  data/literature_reading_prompts.json 220 PDF reading prompts (Task 3)
  data/playground_prompts.json         100 OpenAI Gateway Playground random test prompts
  data/papers_catalog.json            100 curated open-access arXiv papers (Task 3)
  data/scimaster_system_prompt.txt    Bohrium desktop SciMaster system prompt
  data/desktop_tools.json             Exact 16 Bohrium desktop built-in OpenAI tools (Task 2)
internal/bohrium/                 Upstream Bohrium API client & multi-account Manager
  endpoints.go                        Host URLs, constants, header builders, JWT parser, AccountStore interface
  manager.go                          Multi-account Client pool manager (per-account isolation, login scopes, balance sync)
  auth.go                             WeChat QR login, AES-128-CBC password helper, token refresh, club session/enroll
  tasks.go                            Plaza daily task listing, claim-all/claim-one, 4-task classification
  assets.go                           Photons, wallet, expiring lots, ledger, quotas, storage capacity
  papers.go                           PDF selection, cache, arXiv download + mirror fallback, offline fallback
  desktop_task.go                     Task #2 Bohrium desktop client executor (full 16 tools + SciMaster + immediate stream cancel + query-events)
  executor.go                         4-task execution orchestration, query-events telemetry (3-attempt retry), immediate cancel
  scix_llm.go                         Bohrium desktop SciX Sigma LLM client (/models, /billing/preflight, /chat/completions, /responses, per-account storeId)
internal/openai/                  OpenAI-compatible /v1/* gateway (weighted-balance PoolRouter, SQLite 7d bindings, failover, bidirectional bridge, SSE stream + tests)
internal/scheduler/               Multi-account randomized daily scheduler (Asia/Shanghai, per-account jitter + anti-burst gap) + token refresh + balance sync + binding TTL purge
internal/recon/                   Frontend snapshot/diff engine + contract marker scanner + tests
internal/server/                  Admin cookie/header auth, rate limiter, REST API, embedded SPA router + tests
web/                              React + Vite + TypeScript + Tailwind CSS + shadcn/ui frontend (compiled into gitignored web/dist, embedded via web/embed.go)
docs/BINARY-BUILDS.md             Multi-platform static binary & Docker build/release guide
docs/RECON-SNAPSHOT.md            Recon snapshot & drift check guide
docs-site/                        VitePress documentation site (independent npm package)
.github/workflows/                CI, multi-platform binary build/release, Docker build/GHCR push, recon check, VitePress Pages deploy
```

## 4. Upstream API Contract Notes

- **`club.bohrium.com/api/plaza/tasks*`** requires:
  - `Cookie: brmToken=<jwt>`
  - `Origin: https://club.bohrium.com`
  - `Referer: https://club.bohrium.com/`
  - `X-Plaza-Daily-Task-Gateway: production`
  - Missing `Origin`/`Referer` triggers HTTP 403.
- **`sb-cloud.dp.tech/api/v1/chat`**:
  - Query string: `?historyCapabilities=sessionForkHistoryV1`.
  - Auth: `Authorization: Bearer <brmToken>`.
  - Do **not** send `x-shrimp-client-surface` or `Idempotency-Key` to `sb-cloud.dp.tech` (only include them when falling back to `agent.bohrium.com`).
- **Recon HTML Diff Normalization**:
  - Bohrium's CDN injects a per-request `clientSignal="..."` attribute into entry HTML. `internal/recon/snapshot.go` normalizes `clientSignal` and `nonce` before hashing HTML so `recon diff` reports genuine frontend updates only.
  - First-party `cdn.bohrium.com/platform/web/*` scripts are crawled alongside `club.bohrium.com` and `platform.bohrium.com` to verify login contract markers.
- **Bohrium Desktop SciX LLM Gateway (`www.bohrium.com/bohrapi/v1/sigma-search/api/v1/scix/llm`)**:
  - Model catalog: `GET /models?tag=shrimp-brain-v2` with `Authorization: Bearer <brmToken>`.
  - Billing preflight: `POST /billing/preflight` with `{"model":"<id>"}`.
  - Completions & Responses: `POST /chat/completions` and `POST /responses` require `Authorization: Bearer <brmToken>`, `traceparent` (`00-<32hex>-<16hex>-01`), and unpadded-base64url `x-scix-execution` JSON with `"version":1` and `"scenario":"local_agent"` plus canonical UUIDs (`storeId`, `rootSessionId`, `sessionId`, `callId`, `rootRunId`, `runId`, `purpose`, `recoveryAttempt`, `sessionCreatedAt`, `question`).
  - Upstream enforces per-model endpoint routing (`api: "chat_completions"` models reject `/responses`, omitted `api` models speak stateless `/responses` with `store: false`); `internal/openai` transparently bridges both directions while keeping upstream requests 100% native.
  - Streaming LLM calls MUST use `chromehttp.Client.DoStream` (`Timeout: 0`, `ResponseHeaderTimeout: 3m`) so long reasoning/generation streams are never terminated by the 45s non-streaming timeout.

## 5. Build, Test, and Verification

Run all checks from the repository root before committing changes:

```bash
# Frontend build (writes web/dist for go:embed; web/dist/.gitkeep keeps fresh Go builds/tests working without Node)
npm ci --prefix web
npm run build --prefix web

# Static analysis
go vet ./...

# Unit and integration tests (use -race on platforms with CGO/race detector support)
go test -v ./...

# Static single-binary build
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.Version=dev" -o dist/bohrium-club-auto ./cmd/bohrium-club-auto

# Recon snapshot & drift verification (writes only to gitignored /recon/)
./dist/bohrium-club-auto recon pull
./dist/bohrium-club-auto recon diff

# Container build & local compose
docker build -t bohrium-club-auto:local .
docker compose up -d --build
```

## 6. Repository Maintenance Gotchas

- **Keep root-anchored ignore patterns**: In both `.gitignore` and `.dockerignore`, `/data/`, `/recon/`, `/dist/`, and `/bin/` MUST retain their leading slash so `internal/prompts/data/` and `internal/recon/` stay tracked in Git and included in Docker build contexts.
- **GitHub Actions macOS runners**: Keep `macos-15-intel` and `macos-15` in `build-binaries.yml` for native smoke testing (`macos-13` and `macos-14` are retired), while `publish-binaries.yml` cross-compiles all 5 pure-Go `CGO_ENABLED=0` targets on `ubuntu-latest` to avoid hosted macOS arm64 runner capacity timeouts.
- **Timezone data**: `cmd/bohrium-club-auto/main.go` imports `_ "time/tzdata"` so `Asia/Shanghai` works reliably across minimal containers and Windows without host tzdata.
- **UI Safety**: React JSX automatically escapes rendered values; never use `dangerouslySetInnerHTML` with upstream strings in `web/src/`.
- **Documentation Sync**: Whenever environment variables, API endpoints, workflows, or recon markers change, update `README.md`, `config/config.example.json`, and `docs/RECON-SNAPSHOT.md` in the same commit.
