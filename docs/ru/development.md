# Разработка

## Требования

- Go `1.26.3`;
- Deno `2.x` для lint статического Web UI;
- `golangci-lint` `2.x` для Go linting;
- Docker для проверки image/runtime сценариев.

## Основные команды

```bash
make test
make vet
make build
make lint-go
make lint-js
make ci
make docker-build
```

## Локальный запуск UI

Для локальной разработки обычно не нужно применять runtime tunnel changes:

```bash
CONFIG_DIR=/private/tmp/awg-forge-dev \
WEBUI_HOST=127.0.0.1 \
WEBUI_PORT=51821 \
PASSWORD=test \
APPLY_CONFIG=false \
SERVER_HOST=127.0.0.1 \
go run ./cmd/awg-forge serve
```

Открой:

```text
http://127.0.0.1:51821
```

## Проверки Перед Коммитом

```bash
make ci
git diff --check
```

`make ci` запускает:

- `go test ./...`;
- `go vet ./...`;
- `go build ./...`;
- `golangci-lint run`;
- `deno lint`.

## Frontend

Frontend находится в:

- `internal/server/static/index.html`;
- `internal/server/static/app.css`;
- `internal/server/static/app.js`.

Frontend остается статическим HTML/CSS/JavaScript без Node, npm, React, Vue или build pipeline.

Deno используется только как dev/CI-инструмент для lint `internal/server/static/app.js`. Runtime и Docker image не требуют Deno.

## Backend

Основные зоны:

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
