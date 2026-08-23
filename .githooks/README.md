# Git hooks

Plain POSIX shell, no framework (Husky is Node-only and doesn't fit a
zero-dependency Go project; this does the same job as a single Go
binary/`go install` away, at zero cost).

- **pre-commit** — runs `make check` (fmt, vet, build, race tests,
  staticcheck, golangci-lint, govulncheck, a short fuzz smoke run).
- **commit-msg** — enforces Conventional Commits on the subject line.

## Enable

```sh
make install-hooks
```

`core.hooksPath` is a local git config, not something a commit can force
onto a clone — every clone opts in once.

## Skip once

```sh
git commit --no-verify
```
