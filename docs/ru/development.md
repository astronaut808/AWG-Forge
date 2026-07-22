# Разработка

## Требования

- Go `1.26.5`;
- Node.js `24.x` и npm для сборки Web UI;
- Deno `2.x` для lint frontend source;
- `golangci-lint` `2.x` для Go linting;
- Docker для проверки image/runtime сценариев.

## Основные команды

```bash
make test
make vet
make build
make ui-check
make ui-build
make lint-go
make lint-js
make quality
make ci
make security
make security-fast
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
- `npm run ui:check`;
- `npm run ui:build`;
- `deno lint web/src`;
- `npm run quality:aislop`, который запускает `aislop ci` с проектным `.aislop/config.yml`.

Aislop CI gate сейчас падает при score ниже `80`. Config исключает воспроизводимые generated Web UI assets и словари локализации, которые дают scanner-only шум. Source warnings стоит оставлять видимыми, если finding не является документированным false positive.

## Security Checks

Перед публикацией версии запусти release security gate:

```bash
make security
```

`make security` запускает `gitleaks`, `trivy` и Semgrep registry rules. Команде может понадобиться доступ к сети для баз сканеров и правил.

Для более быстрой локальной проверки:

```bash
make security-fast
```

Generated frontend assets в `internal/server/static/assets/` и встроенные fonts исключены из Semgrep. Source of truth для UI — `web/src/`; generated output проверяется через `npm run ui:build` и `git diff --exit-code -- internal/server/static`.

## Frontend

Frontend source находится в `web/` и собирается через Vite + Preact + TypeScript.

Generated output находится в `internal/server/static/` и встраивается в Go-бинарь через `embed.FS`. Эти файлы нужно обновлять командой:

```bash
npm install
npm run ui:build
```

Для dev-сервера frontend:

```bash
npm run ui:dev
```

`ui:dev` проксирует `/api` и `/clients` на локальный backend `127.0.0.1:51821`.

Runtime и Docker image не требуют Node/npm/Deno. Эти инструменты нужны только для разработки и CI.

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
