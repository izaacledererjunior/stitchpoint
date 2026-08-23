.PHONY: build test race bench lint vet staticcheck vulncheck fuzz check tools clean

build:
	go build ./...

test:
	go test ./...

race:
	go test -race ./...

bench:
	go test ./... -bench=. -benchmem -run '^$$'

vet:
	go vet ./...

# Versions pinned to match .github/workflows/ci.yml — keep both in sync.
staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.1 run ./...

vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...

# Short local smoke run; use `go test -fuzz FuzzParse -fuzztime 5m` (or
# longer) directly for a real fuzzing session.
fuzz:
	go test ./internal/scte35/... -run '^$$' -fuzz FuzzParse -fuzztime 15s

# Everything CI runs, in one command — the same checks a PR is gated on,
# runnable locally before pushing.
check: vet build race staticcheck lint vulncheck fuzz
	@echo "all checks passed"

clean:
	go clean ./...
	rm -rf bin
