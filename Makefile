COMPOSE ?= docker compose
CONTAINER ?= awg-forge
IMAGE ?= awg-forge:local
GITLEAKS_LOG_OPTS ?= HEAD
GOVULNCHECK_VERSION ?= v1.1.4
ACTIONLINT_VERSION ?= v1.7.12

.PHONY: test test-race test-shell vet build lint-go lint-js lint-shell lint-docker lint-actions lint-actions-security quality ui-build ui-check ui-test api-contract ci vuln-check security security-fast docker-smoke updates updates-local updates-docker update-amneziawg-refs docker-build docker-up docker-down

test:
	go test ./...

test-race:
	go test -race ./...

test-shell:
	bash -n install.sh uninstall.sh scripts/*.sh
	bash scripts/test-install.sh
	bash scripts/test-install-dependencies.sh
	bash scripts/test-upgrade.sh
	bash scripts/test-uninstall.sh
	bash scripts/test-release-workflow.sh

vet:
	go vet ./...

build:
	go build ./...

lint-go:
	golangci-lint run

lint-js:
	npm run ui:lint

lint-shell:
	shellcheck --severity=warning install.sh uninstall.sh scripts/*.sh

lint-docker:
	hadolint Dockerfile

lint-actions:
	go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

lint-actions-security:
	zizmor --offline --pedantic .

quality:
	npm run quality:aislop

ui-check:
	npm run ui:check

ui-build:
	npm run ui:build

ui-test:
	npm run ui:test

api-contract:
	npm run api:contract
	go test ./internal/server -run '^Test(API(ErrorResponseContract|ContractRegenerateProtocolRejectsMalformedJSON)|Idempotency|OpenAPIContract)'

ci: ui-check ui-test api-contract test test-shell vet build lint-go lint-js quality

vuln-check:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
	GOVULNCHECK_VERSION=$(GOVULNCHECK_VERSION) bash scripts/check-amneziawg-runtime-vulns.sh

security:
	$(MAKE) vuln-check lint-shell lint-docker lint-actions lint-actions-security
	gitleaks git --redact --no-banner --log-opts="$(GITLEAKS_LOG_OPTS)" .
	trivy fs --scanners vuln --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 .
	semgrep --config=auto --disable-version-check --error .

security-fast:
	$(MAKE) vuln-check lint-shell lint-docker lint-actions lint-actions-security
	gitleaks git --redact --no-banner --log-opts="$(GITLEAKS_LOG_OPTS)" .
	trivy fs --scanners vuln --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 --quiet .
	semgrep --config=p/golang --config=p/typescript --disable-version-check --error .

docker-smoke:
	IMAGE=$(IMAGE) bash scripts/test-docker-image.sh

updates: updates-local

updates-local:
	set -a; . ./build/amneziawg.refs; set +a; go run ./cmd/awg-forge updates

updates-docker:
	docker exec $(CONTAINER) awg-forge updates

update-amneziawg-refs:
	./scripts/update-amneziawg-refs.sh

docker-build:
	docker build -t awg-forge:local .

docker-up:
	$(COMPOSE) up -d

docker-down:
	$(COMPOSE) down
