# Разработка

## Требования

- Go `1.26.7`;
- Node.js `24.x` и npm для сборки Web UI;
- Deno `2.x` для lint frontend source;
- `golangci-lint` `2.x` для Go linting;
- Docker для проверки image/runtime сценариев.

## Основные команды

```bash
make test
make test-race
make vet
make build
make ui-check
make ui-build
make ui-test
make api-contract
make lint-go
make lint-js
make lint-shell
make lint-docker
make lint-actions
make lint-actions-security
make quality
make ci
make security
make security-fast
make docker-build
make docker-smoke IMAGE=awg-forge:local
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

Стандартный Docker-образ содержит экспериментальный userspace runtime AWG 3.x.
Для локальной проверки UI без применения конфигов явно включи ту же compiled capability:

```bash
CONFIG_DIR=/private/tmp/awg-forge-dev \
WEBUI_HOST=127.0.0.1 \
WEBUI_PORT=51821 \
PASSWORD=test \
APPLY_CONFIG=false \
go run -ldflags='-X github.com/astronaut808/awg-forge/internal/buildinfo.AWG3Runtime=true' ./cmd/awg-forge serve
```

Оставь `APPLY_CONFIG=false`. Локальный `go run` не устанавливает закреплённые
runtime tools AWG 3.x, которые входят в стандартный Docker-образ.

## Проверки перед коммитом

После `npm ci` установи закреплённый браузер один раз; повтори установку после обновления Playwright:

```bash
npx --no-install playwright install chromium
```

На Linux добавь `--with-deps`, если не хватает системных библиотек браузера.

```bash
make ci
git diff --check
```

`make ci` запускает:

- `go test ./...`;
- `make test-shell`, проверяющий сценарии установки, обновления, удаления и release-workflow;
- `go vet ./...`;
- `go build ./...`;
- `golangci-lint run`;
- `npm run ui:check`;
- `npm run ui:test`, который собирает встроенный UI и выполняет регрессионные тесты Chromium;
- `make api-contract`, который разбирает исходный OpenAPI-документ и проверяет описанные core control-plane routes и envelope ошибок;
- `npm run ui:lint`, проверяющий исходники frontend и браузерные тесты;
- `npm run quality:aislop`, который запускает `aislop ci` с проектным `.aislop/config.yml`.

Для pull request отдельно запускаются jobs `Security`, `Race` и проверка Docker-образа. Security job выполняет `govulncheck`, Gitleaks, focused Semgrep и Trivy filesystem scans, ShellCheck, Hadolint, actionlint и offline pedantic-аудит zizmor. Docker job запускает собранный образ с отключённым runtime apply, выполняет вход в API, проверяет встроенные AmneziaWG binaries и parser сгенерированного конфига, перезапускает контейнер и убеждается, что конфигурация туннеля осталась доступной и корректно разбирается.

Aislop CI gate сейчас падает при score ниже `80`. Config исключает воспроизводимые generated Web UI assets и словари локализации, которые дают scanner-only шум. Source warnings стоит оставлять видимыми, если finding не является документированным false positive.

## Security Checks

Перед публикацией версии запусти release security gate:

```bash
make security
```

`make security` запускает `govulncheck` для AWG-Forge и корневого daemon package из точного `AMNEZIAWG_GO_REF`, а также ShellCheck, Hadolint, actionlint, zizmor, Gitleaks, Trivy и полный набор Semgrep registry rules. Команде может понадобиться доступ к сети для pinned upstream source, Go tools, баз сканеров и правил. Zizmor дополняет actionlint и проверяет permissions, опасные triggers, mutable action references, обработку untrusted input и другие security-свойства GitHub Actions.

Для более быстрой локальной проверки:

```bash
make security-fast
```

Быстрый gate использует focused Semgrep rules и findings Trivy уровня HIGH/CRITICAL. Локально Gitleaks проверяет историю, достижимую из `HEAD`; в pull request проверка ограничена диапазоном коммитов PR. Проверка Docker-образа блокирует исправляемые HIGH/CRITICAL уязвимости пакетов операционной системы. Findings в application dependencies остаются информационными; blocking source checks отдельно определяют достижимость в AWG-Forge и точном pinned AmneziaWG daemon, не отклоняя неиспользуемые packages из более широкого upstream module.

Два Semgrep findings, требующие финальный non-root `USER`, подавляются точечно только на соответствующих инструкциях `ENTRYPOINT` и `CMD` runtime Dockerfile. AWG-Forge намеренно запускается от root с capabilities, выданными через Compose, поскольку управляет TUN-интерфейсами, маршрутами и firewall в host network namespace. Для безопасного отказа от root потребуется вынести эти операции в отдельный привилегированный helper; repository-wide исключения правил скрыли бы findings в будущих Dockerfiles и не допускаются.

После локальной сборки можно запустить тот же непривилегированный image smoke test, который используется в pull request:

```bash
make docker-build
make docker-smoke IMAGE=awg-forge:local
```

Generated frontend assets в `internal/server/static/assets/` и встроенные fonts исключены из Semgrep. Source of truth для UI — `web/src/`; generated output проверяется через `npm run ui:build` и `git diff --exit-code -- internal/server/static`.

## Frontend

`make ui-test` проверяет вход и выход, сохранение языка и темы, формы клиентов и туннелей, ошибки внутри форм, обслуживание, экспорт и кэш QR для AWG 2.0/3.x. Chromium запускается с настольным и мобильным размером окна, светлой/тёмной темой и английским/русским UI. Тесты используют настоящие HTTP handlers и временные данные при `APPLY_CONFIG=false`; они не подтверждают передачу VPN-трафика или совместимость нативных мобильных клиентов.

Runner запускает изолированные loopback-backend на портах `51924`–`51927` и удаляет их временные каталоги при штатном завершении. `UI_TEST_PORT` меняет первый порт диапазона. Существующие серверы не переиспользуются, настройки окружения рабочего развёртывания не передаются в backend. Traces, скриншоты и видео в CI отключены, поскольку экспорты содержат ключи; отчёты тестов исключены из Git и не загружаются как артефакты.

Frontend source находится в `web/` и собирается через Vite + Preact + TypeScript.

Generated output находится в `internal/server/static/` и встраивается в Go-бинарь через `embed.FS`. Эти файлы нужно обновлять командой:

```bash
npm ci
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

`internal/protocol` владеет упорядоченным реестром профилей, их ID, отображаемыми именами, версиями, редактируемыми параметрами, значениями по умолчанию, валидацией и рендерингом. `internal/server` владеет только представлением для браузера: вкладками, групповыми метками, признаком экспериментального профиля и runtime-доступностью. Frontend получает имя и статус профиля из `/api/state` и не должен поддерживать собственную таблицу соответствия profile ID. При добавлении профиля его нужно один раз зарегистрировать в `internal/protocol`, добавить server presentation metadata и расширить тесты полноты реестра и API-каталога.
