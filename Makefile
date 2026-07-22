COMPOSE ?= docker compose
CONTAINER ?= awg-forge

.PHONY: test test-shell vet build lint-go lint-js quality ui-build ui-check ci security security-fast updates updates-local updates-docker update-amneziawg-refs docker-build docker-up docker-down

test:
	go test ./...

test-shell:
	bash -n install.sh uninstall.sh scripts/*.sh
	bash scripts/test-install.sh
	bash scripts/test-upgrade.sh
	bash scripts/test-uninstall.sh

vet:
	go vet ./...

build:
	go build ./...

lint-go:
	golangci-lint run

lint-js:
	npm run ui:lint

quality:
	npm run quality:aislop

ui-check:
	npm run ui:check

ui-build:
	npm run ui:build

ci: ui-check ui-build test test-shell vet build lint-go lint-js quality

security:
	gitleaks detect --source=. --no-banner
	trivy fs .
	semgrep --config=auto --disable-version-check .

security-fast:
	gitleaks detect --source=. --no-banner
	trivy fs --severity HIGH,CRITICAL --quiet .
	semgrep --config=p/golang --config=p/typescript --config=p/secrets --disable-version-check .

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
