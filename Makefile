.PHONY: check fmt test test-integration run docker-up docker-down

check:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go test -race ./...

fmt:
	gofmt -w cmd internal migrations tests

test:
	go test ./...

test-integration:
	test -n "$$ASTER_TEST_DATABASE_URL"
	go test -count=1 ./tests

run:
	go run ./cmd/server

docker-up:
	docker compose -f deployments/docker/compose.yaml up --build

docker-down:
	docker compose -f deployments/docker/compose.yaml down
