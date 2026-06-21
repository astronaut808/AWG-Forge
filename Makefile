COMPOSE ?= docker compose
CONTAINER ?= awg-forge

.PHONY: test test-shell vet build lint-go lint-js ui-build ui-check ci updates updates-local updates-docker update-amneziawg-refs docker-build docker-up docker-down

test:
	go test ./...

test-shell:
	bash -n install.sh uninstall.sh scripts/*.sh
	bash scripts/test-install.sh
	bash scripts/test-uninstall.sh

vet:
	go vet ./...

build:
	go build ./...

lint-go:
	golangci-lint run

lint-js:
	npm run ui:lint

ui-check:
	npm run ui:check

ui-build:
	npm run ui:build

ci: ui-check ui-build test test-shell vet build lint-go lint-js

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
