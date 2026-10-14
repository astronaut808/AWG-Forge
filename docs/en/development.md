# Development

## Branches and Pull Requests

Create feature and bug-fix branches from the latest `develop`, and target
`develop` when opening a pull request. GitHub may suggest the default branch,
`master`; change the base to `develop` before submitting. `master` is reserved
for releases. See [CONTRIBUTING.md](../../CONTRIBUTING.md) for contribution and
validation requirements.

## Requirements

- Go `1.26.9`;
- Node.js `24.21.0` and npm for building the Web UI;
- Deno `2.x` for frontend source linting;
- `golangci-lint` `2.x` for Go linting;
- Docker for image/runtime testing.

## Common Commands

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
make lint-markdown
make lint-shell
make lint-docker
make lint-actions
make lint-actions-security
make npm-audit
make quality
make ci
make security
make security-fast
make docker-build
make docker-smoke IMAGE=awg-forge:local
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

The standard Docker image includes the experimental AWG 3.x userspace runtime.
For a local no-apply UI review, expose the same compiled capability explicitly:

```bash
CONFIG_DIR=/private/tmp/awg-forge-dev \
WEBUI_HOST=127.0.0.1 \
WEBUI_PORT=51821 \
PASSWORD=test \
APPLY_CONFIG=false \
go run -ldflags='-X github.com/astronaut808/awg-forge/internal/buildinfo.AWG3Runtime=true' ./cmd/awg-forge serve
```

Keep `APPLY_CONFIG=false`. A local `go run` does not install the pinned AWG 3.x
runtime tools shipped in the standard Docker image.

## Pre-commit Checks

Install the pinned browser once after `npm ci` (repeat after Playwright updates):

```bash
npx --no-install playwright install chromium
```

On Linux, add `--with-deps` if browser system libraries are missing.

```bash
make ci
git diff --check
```

`make ci` runs:

- `go test ./...`;
- `make test-shell`, covering installer, upgrade, uninstall, and release-workflow scenarios;
- `go vet ./...`;
- `go build ./...`;
- `golangci-lint run`;
- `npm run ui:check`;
- `npm run ui:test`, which builds the embedded UI and runs Chromium regression tests;
- `make api-contract`, which parses the OpenAPI source and checks the documented core control-plane routes and error envelope;
- `npm run ui:lint`, covering frontend source and browser tests;
- `make lint-markdown`, checking Markdown structure and references;
- `npm run quality:aislop`, which runs `aislop ci` with the project `.aislop/config.yml`.

The GitHub `CI` workflow runs `Go checks` and `UI checks` in parallel on separate
runners. `Go checks` covers formatting, Go tests, shell tests, vet, build, and Go
lint. `UI checks` covers Markdown lint, UI typechecking and build, the API
contract, generated UI reproducibility, browser tests, UI lint, and Aislop.

The aggregate check keeps the existing name `Go`. It waits for both jobs and
succeeds only when both succeed; a failed, cancelled, or skipped dependency
cannot produce a successful aggregate. Keep `Go` as the required CI check in
branch protection. Local `make ci` still runs the full set of checks; browser
tests use four workers; validate their timing and stability on the CI runner.

Pull requests also run separate `Security`, `Race`, and Docker image validation jobs. The security job runs `govulncheck`, npm audit, Gitleaks, focused Semgrep and Trivy filesystem scans, ShellCheck, Hadolint, actionlint, and offline pedantic zizmor analysis. The Docker job starts the built image with runtime apply disabled, authenticates to the API, verifies the bundled AmneziaWG binaries and rendered config parser, restarts the container, and checks that the tunnel configuration remains available and parseable.

The Aislop CI gate currently fails below score `80`. The config excludes reproducible generated Web UI assets and locale dictionaries that produce scanner-only noise. Keep source warnings visible unless a finding is a documented false positive.

Weekly Dependabot version updates for Go modules, GitHub Actions, npm, and Docker target `develop`. GitHub's Dependabot security updates still target the repository's default branch, `master`, regardless of `target-branch`.

## Markdown Checks

After `npm ci`, run `make lint-markdown` or `npm run docs:lint`. The same check
is included in `make quality`, `make ci`, and the GitHub CI workflow.

The pinned `markdownlint-cli` uses `.markdownlint.jsonc` with an explicit
rule allowlist. It checks heading and block structure, trailing whitespace,
copyable command examples, fenced code languages, empty/reversed links, local
fragments, reference labels, image alt text, and table column counts. It does not
check whether external URLs or links to other files exist.

Line length, list numbering/marker style, emphasis style, raw HTML, bare URLs,
and visual table alignment are not enforced. Repeated headings are allowed under
different parents, including release sections in `CHANGELOG.md`. Tabs inside code
examples and intentional bracketed text are preserved. New rules require an
explicit configuration change; they are not enabled automatically by an update.

The check covers Markdown files throughout the repository, respects `.gitignore`,
and excludes dependencies, generated UI assets, and local tool directories.
It reports errors without rewriting files; automatic fixes are not part of CI.

Scoped npm overrides keep the Markdown tool's `smol-toml`, `js-yaml`, and KaTeX dependencies
on fixed versions. Review these overrides when updating the tool and run
`make npm-audit` before removing them.

## Security Checks

Run the release security gate before publishing a version:

```bash
make security
```

`make security` runs `govulncheck` against AWG-Forge and the root daemon package at the exact `AMNEZIAWG_GO_REF`, plus ShellCheck, Hadolint, actionlint, zizmor, Gitleaks, Trivy, and the full Semgrep registry rules. Both security gates also audit the complete npm lock, including development dependencies, and reject MODERATE or higher findings. It may need network access for the pinned upstream source, npm audit, Go tools, scanner databases, and rules. Zizmor complements actionlint by checking workflow permissions, unsafe triggers, mutable action references, untrusted input handling, and other GitHub Actions security properties.

`make npm-audit` runs the same lockfile check independently. It explicitly includes development, optional, and peer dependencies and forces an online audit even if npm's `offline` setting is enabled. An unavailable audit registry fails the check.

For a faster local check:

```bash
make security-fast
```

The fast gate uses focused Semgrep rules and HIGH/CRITICAL Trivy findings. Gitleaks scans history reachable from `HEAD` locally; pull requests restrict it to the PR commit range. Docker image validation blocks fixed HIGH/CRITICAL operating-system package vulnerabilities. Application dependency findings remain informational; the blocking source checks separately determine reachability in AWG-Forge and the exact pinned AmneziaWG daemon instead of rejecting unused packages from the wider upstream module.

The two Semgrep findings that require a final non-root `USER` are suppressed only on the affected `ENTRYPOINT` and `CMD` instructions in the runtime Dockerfile. AWG-Forge intentionally runs as root with the Compose-granted capabilities because it manages host-network TUN interfaces, routes, and firewall rules. Removing root safely requires splitting those operations into a separate privileged helper; repository-wide rule exclusions would hide findings in future Dockerfiles and are not permitted.

Run the same non-privileged image smoke test used for pull requests after building a local image:

```bash
make docker-build
make docker-smoke IMAGE=awg-forge:local
```

Generated frontend assets under `internal/server/static/assets/` and embedded fonts are excluded from Semgrep. The source of truth is `web/src/`; generated output is verified by `npm run ui:build` and `git diff --exit-code -- internal/server/static`.

## Frontend

`make ui-test` exercises login/logout, saved language/theme, client and tunnel forms, inline failures, maintenance views, and AWG 2.0/3.x export and QR-cache behavior. Chromium runs at desktop and mobile viewport sizes in light/dark themes with English/Russian UI. It uses real HTTP handlers and temporary data, with `APPLY_CONFIG=false`; it does not prove VPN traffic or native mobile-client interoperability.

The runner starts isolated loopback backends on ports `51924` through `51927` and removes their temporary directories on normal shutdown. Set `UI_TEST_PORT` to change the first port. Existing servers are never reused, and deployment environment settings are not passed to the backends. Browser traces, screenshots, and videos are disabled in CI because exports contain keys; test reports are ignored by Git and are not uploaded.

Frontend source lives in `web/` and is built with Vite + Preact + TypeScript.

Generated output lives in `internal/server/static/` and is embedded into the Go binary with `embed.FS`. Update generated files with:

```bash
npm ci
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

`internal/protocol` owns the ordered profile registry, profile IDs, display names, versions, editable parameter keys, defaults, validation, and rendering. `internal/server` owns browser-only presentation metadata such as tabs, grouping labels, the experimental marker, and runtime exposure. The frontend consumes profile names and status from `/api/state` and must not maintain its own profile-ID mapping. When adding a profile, register it once in `internal/protocol`, add its server presentation metadata, and extend the registry/catalog completeness tests.

## Snapshot refresh checkpoint

[Delivery design](../design/snapshot-refresh-delivery.md) defines the implemented
read-only protocol. Controller migration 12 adds durable operations and archive
validation; restore removes queue state within the authority-reset transaction.
The single node worker owns sequence ordering and pending result retries, including
DB-off. UI source remains in `web/`; regenerate embedded static files with
`npm run ui:build`. Contracts and negative tests cover strict JSON, correlated
success, replay and fencing. Run `make ci`, `make quality`, `make security-fast` and
`go test -race ./...`; local final-image acceptance must include real TLS/mTLS worker
and browser behavior plus Linux-root recovery tests. Native multi-host G12 remains
an independent release requirement.
