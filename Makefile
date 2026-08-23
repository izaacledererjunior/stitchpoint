.PHONY: build test race bench fmt lint vet staticcheck vulncheck fuzz check install-hooks clean

build:
	go build ./...

# Matches the check .github/workflows/ci.yml's gofmt step runs.
fmt:
	@fmt_out="$$(gofmt -l .)"; \
	if [ -n "$$fmt_out" ]; then \
		echo "not gofmt-formatted:"; \
		echo "$$fmt_out"; \
		exit 1; \
	fi

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
check: fmt vet build race staticcheck lint vulncheck fuzz
	@echo "all checks passed"

# Points Git at .githooks (pre-commit runs `make check`, commit-msg
# validates Conventional Commits) — a one-time opt-in per clone, since
# core.hooksPath is a local git config, not something a commit can force
# onto a collaborator's checkout.
install-hooks:
	git config core.hooksPath .githooks
	@echo "git hooks installed (.githooks) — 'git commit --no-verify' skips them for one commit"

clean:
	go clean ./...
	rm -rf bin
