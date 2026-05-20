COMPOSE ?= docker compose
CONTAINER ?= awg-forge

.PHONY: test vet build ci updates updates-local updates-docker update-amneziawg-refs docker-build docker-up docker-down

test:
	go test ./...

vet:
	go vet ./...

build:
	go build ./...

ci: test vet build

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
