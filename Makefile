.PHONY: check test test-race test-integration test-cover test-core-export verify verify-ci build migrate-up

check:
	test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './.git/*'))"
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

test-integration:
	test -n "$$AGENT_TEST_MYSQL_DSN"
	go test -race -count=1 ./examples/ecs/internal/repository/mysql

test-cover:
	go test -coverpkg=./... -coverprofile=/tmp/ergo-agent-coverage.out ./...
	go tool cover -func=/tmp/ergo-agent-coverage.out
	go tool cover -func=/tmp/ergo-agent-coverage.out | awk '/^total:/{gsub("%", "", $$NF); if ($$NF + 0 < 45) exit 1}'

test-core-export:
	core_export_dir="$$(mktemp -d)/ergo-core"; \
	go run ./cmd/export-core -output "$$core_export_dir"; \
	cd "$$core_export_dir"; \
	go mod tidy; \
	go test ./...

verify: check test-race test-core-export build

verify-ci: verify test-integration test-cover

build:
	go build ./examples/ecs/cmd/agent-api
	go build ./examples/ecs/cmd/agent-worker
	go build ./examples/ecs/cmd/agent-migrate

migrate-up:
	go run ./examples/ecs/cmd/agent-migrate
