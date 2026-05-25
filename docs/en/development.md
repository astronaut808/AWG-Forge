# Development

## Requirements

- Go `1.26.3`;
- Deno `2.x` for static Web UI linting;
- Docker for image/runtime testing.

## Common Commands

```bash
make test
make vet
make build
make lint-js
make ci
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
SERVER_HOST=127.0.0.1 \
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
- `deno lint`.

## Frontend

Frontend files:

- `internal/server/static/index.html`;
- `internal/server/static/app.css`;
- `internal/server/static/app.js`.

The frontend remains static HTML/CSS/JavaScript with no Node, npm, React, Vue, or build pipeline.

Deno is used only as a dev/CI tool for linting `internal/server/static/app.js`. The runtime and Docker image do not require Deno.

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
