.PHONY: postgres test race vet build verify run clean

postgres:
	docker compose up -d postgres

test:
	GOTOOLCHAIN=local go test ./... -count=1

race:
	GOTOOLCHAIN=local go test -race ./... -count=1

vet:
	GOTOOLCHAIN=local go vet ./...

build:
	GOTOOLCHAIN=local go build ./...

verify: test race vet build

run:
	GOTOOLCHAIN=local go run ./cmd/server

clean:
	docker compose down -v
