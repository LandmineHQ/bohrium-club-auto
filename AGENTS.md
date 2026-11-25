# AGENTS.md

Repository guidance for automated coding agents and human contributors working on `bohrium-club-auto`.

## 1. Project Overview

`bohrium-club-auto` is a self-hosted **Go 1.24** service (zero third-party Go dependencies) providing:

- Automated & manual daily task execution for **Bohrium User Community (`https://club.bohrium.com/`)**.
- Remote server-side authentication (WeChat Official Account QR scan, `brmToken` import, automatic JWT refresh).
- An embedded, admin-password-protected Web Dashboard (`web/static/*` via `go:embed`) showing remaining photons, expiring lots, ledger history, membership quotas, knowledge-base storage capacity, daily tasks, execution logs, schedule settings, and live frontend recon drift checks.
- A built-in frontend snapshot/diff checker (`recon pull` / `recon diff`) modeled on `LandmineHQ/duckai-cpa` conventions.

## 2. Non-Negotiable Invariants

Follow these rules strictly on every change:

1. **Chrome Browser Outgoing Fingerprint**: All outbound HTTP traffic MUST go through `internal/chromehttp` with a realistic Desktop Chrome `User-Agent`, `Sec-CH-UA`, `Sec-CH-UA-Mobile`, `Sec-CH-UA-Platform`, and `Accept-Language`. Never leak Go's default `Go-http-client`.
2. **Do Not Call Deprecated Check-In**: Never call or re-add the deprecated legacy 200-photon check-in endpoint (`/bohrapi/v1/account/user/integral_receive`).
3. **Skip Desktop Client Task (Task #2)**: Always classify `体验玻尔科学空间「客户端」` (or any client/desktop task) as skipped and non-executable. Never attempt to execute or report `query-events` for it.
4. **Real Web Task Flow + Immediate Termination**:
   - **Task 1 (`bohrium_science_space_web`)**: Pick a random prompt from `internal/prompts/data/science_space_prompts.json`, post to `/api/v1/chat?historyCapabilities=sessionForkHistoryV1` targeting `{"kind":"team","teamId":"AgK4D5B8Fdju"}`, report `POST /api/v1/query-events`, and immediately cancel via `POST /api/v1/chat/<workflowId>/cancel` (`{"source":"api_request"}`).
   - **Task 3 (`bohrium_literature_reading`)**: Pick a random PDF from `internal/prompts/data/papers_catalog.json` (100 curated arXiv papers, cached in `data/pdfs/`, or user PDFs in `data/custom_pdfs/`), upload through `literature-sage` (`folder/create` → `file/multipart` → `/api/upload/binary` with `X-Storage-Param` → `file/submit`), register at `scix/reader/entries`, submit a random question from `literature_reading_prompts.json` with `readerEntryId`, report `query-events`, and immediately cancel the workflow.
   - **Task 4 (`bohrium_academic_writing`)**: Pick a random prompt from `academic_writing_prompts.json`, submit chat with `Referer: https://www.bohrium.com/academic-writing`, report `query-events`, and immediately cancel the workflow.
5. **No CAPTCHA Bypass**: Password login (`platform.bohrium.com/api/account/v3/login`) is only attempted when `/api/captcha/check_user` explicitly returns `skip: true`. Never implement CAPTCHA solving or bypass logic; guide users to WeChat QR login or `brmToken` import.
6. **Admin Authentication Gate**: Keep all operational `/api/*` endpoints behind `requireAdmin` in `internal/server/server.go`. Only `/healthz`, static SPA assets, and `/api/auth/{status,login,logout}` are publicly reachable.
7. **No Vendored Upstream Recon or Runtime Secrets**: Never commit `/recon/`, `/data/`, tokens, passwords, or downloaded upstream bundles to Git.
8. **Zero Third-Party Go Dependencies**: Prefer Go standard library packages so `CGO_ENABLED=0` static cross-compilation and minimal Alpine Docker builds remain fast, reproducible, and self-contained.

## 3. Repository Layout

```text
cmd/bohrium-club-auto/main.go     CLI entry point (serve, recon pull, recon diff, version)
internal/chromehttp/              Chrome UA & Client Hints HTTP client wrapper + tests
internal/config/                  Atomic config.json management, env overrides, first-boot password
internal/store/                   Atomic state.json persistence (auth state, schedule day, logs)
internal/prompts/                 go:embed loader for prompt corpora & 100-paper catalog + tests
  data/science_space_prompts.json     330 scientific prompts (Task 1)
  data/academic_writing_prompts.json  320 academic writing prompts (Task 4)
  data/literature_reading_prompts.json 220 PDF reading prompts (Task 3)
  data/papers_catalog.json            100 curated open-access arXiv papers (Task 3)
internal/bohrium/                 Upstream Bohrium API client
  endpoints.go                        Host URLs, constants, header builders, JWT parser
  auth.go                             WeChat QR login, AES-128-CBC password helper, token refresh, club session/enroll
  tasks.go                            Plaza daily task listing, claim-all/claim-one, task classification
  assets.go                           Photons, wallet, expiring lots, ledger, quotas, storage capacity
  papers.go                           PDF selection, cache, arXiv download + mirror fallback, offline fallback
  executor.go                         3-task execution orchestration, query-events telemetry, immediate cancel
internal/scheduler/               Daily timezone-aware scheduler (Asia/Shanghai) + periodic token refresh
internal/recon/                   Frontend snapshot/diff engine + contract marker scanner + tests
internal/server/                  Admin cookie/header auth, rate limiter, REST API, embedded SPA router + tests
web/                              go:embed static Dashboard (index.html, styles.css, app.js)
docs/RECON-SNAPSHOT.md            Recon snapshot & drift check guide
.github/workflows/                CI, multi-platform binary build/release, Docker build/GHCR push, recon check
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

## 5. Build, Test, and Verification

Run all checks from the repository root before committing changes:

```bash
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
- **GitHub Actions macOS runners**: Keep `macos-15-intel` and `macos-15` in binary build/publish workflows (`macos-13` and `macos-14` are retired).
- **Timezone data**: `cmd/bohrium-club-auto/main.go` imports `_ "time/tzdata"` so `Asia/Shanghai` works reliably across minimal containers and Windows without host tzdata.
- **UI Safety**: Render dynamic values in `web/static/app.js` through `escapeHTML(...)` or `textContent`, never raw `innerHTML` with unescaped upstream strings.
- **Documentation Sync**: Whenever environment variables, API endpoints, workflows, or recon markers change, update `README.md`, `config/config.example.json`, and `docs/RECON-SNAPSHOT.md` in the same commit.
