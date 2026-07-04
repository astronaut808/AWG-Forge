# Development

## Requirements

- Go `1.26.4`;
- Node.js `24.x` and npm for building the Web UI;
- Deno `2.x` for frontend source linting;
- `golangci-lint` `2.x` for Go linting;
- Docker for image/runtime testing.

## Common Commands

```bash
make test
make vet
make build
make ui-check
make ui-build
make lint-go
make lint-js
make ci
make security
make security-fast
make docker-build
```

## Local UI Run

For local development, runtime tunnel changes usually do not need to be applied:

```bash
CONFIG_DIR=/private/tmp/awg-forge-dev \
WEBUI_HOST=127.0.0.1 \
WEBUI_PORT=51821 \
PASSWORD=test \
APPLY_CONFIG=false \
go run ./cmd/awg-forge serve
```

Open:

```text
http://127.0.0.1:51821
```

## Pre-commit Checks

```bash
make ci
git diff --check
```

`make ci` runs:

- `go test ./...`;
- `go vet ./...`;
- `go build ./...`;
- `golangci-lint run`;
- `npm run ui:check`;
- `npm run ui:build`;
- `deno lint web/src`.

## Security Checks

Run the release security gate before publishing a version:

```bash
make security
```

`make security` runs `gitleaks`, `trivy`, and Semgrep registry rules. It may need network access for scanner databases and rules.

For a faster local check:

```bash
make security-fast
```

Generated frontend assets under `internal/server/static/assets/` and embedded fonts are excluded from Semgrep. The source of truth is `web/src/`; generated output is verified by `npm run ui:build` and `git diff --exit-code -- internal/server/static`.

## Frontend

Frontend source lives in `web/` and is built with Vite + Preact + TypeScript.

Generated output lives in `internal/server/static/` and is embedded into the Go binary with `embed.FS`. Update generated files with:

```bash
npm install
npm run ui:build
```

For frontend dev server:

```bash
npm run ui:dev
```

`ui:dev` proxies `/api` and `/clients` to the local backend at `127.0.0.1:51821`.

The runtime and Docker image do not require Node/npm/Deno. These tools are development and CI-only.

## Backend

Main areas:

- `cmd/awg-forge`: CLI entrypoint;
- `internal/app`: service layer, state mutations, rollback, rendering/apply orchestration;
- `internal/backup`: encrypted backup and restore validation;
- `internal/config`: env/state model;
- `internal/firewall`: managed iptables check/repair model;
- `internal/protocol`: protocol profiles and validation;
- `internal/render`: server/client config rendering;
- `internal/server`: Web UI/API;
- `internal/doctor`: diagnostics;
- `internal/support`: secret-free support bundle generation;
- `internal/updates`: AmneziaWG upstream update checks.
