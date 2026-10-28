#!/bin/bash
# The same verification entry point for local test builds and release CI.
set -euo pipefail
cd "$(dirname "$0")/.."

unformatted=$(gofmt -l .)
if [[ -n "$unformatted" ]]; then
  printf 'Run gofmt on:\n%s\n' "$unformatted" >&2
  exit 1
fi
go mod verify
go vet ./...
go test -race ./...
python3 scripts/benchmark-nanobrew-test.py
go run ./scripts/check-tutorial.go
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

commit=$(git rev-parse HEAD)
if [[ -n $(git status --porcelain) ]]; then
  commit="$commit-dirty"
fi
build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
mkdir -p bin
CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=dev -X main.commit=$commit -X main.date=$build_date" \
  -o bin/gobrew ./cmd/gobrew
scripts/test-bootstrap.sh "$PWD/bin/gobrew"
shasum -a 256 bin/gobrew > bin/gobrew.sha256
printf '\nLocal checks passed. Test build: %s/bin/gobrew\n' "$PWD"
printf 'Commit: %s\nInstall when ready: ./bin/gobrew self install\n' "$commit"
