COMPOSE ?= docker compose
CONTAINER ?= awg-forge

.PHONY: test test-shell vet build lint-go lint-js quality ui-build ui-check ci security security-fast updates updates-local updates-docker update-amneziawg-refs docker-build docker-build-awg3 docker-up docker-down

AWG3_GO_REF ?= 9f5d948bc72cc554791cfe0fb91527e4acfb6b79
AWG3_TOOLS_REF ?= 05434cab7d91bbbc607d18ec5fade91f4b83774c

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

docker-build-awg3:
	docker build --build-arg AWG3_EXPERIMENTAL=true --build-arg AMNEZIAWG_GO_REF_OVERRIDE=$(AWG3_GO_REF) --build-arg AMNEZIAWG_TOOLS_REF_OVERRIDE=$(AWG3_TOOLS_REF) -t awg-forge:awg3-experimental .

docker-up:
	$(COMPOSE) up -d

docker-down:
	$(COMPOSE) down
