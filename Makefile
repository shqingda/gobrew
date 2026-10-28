.PHONY: check build

check:
	./scripts/ci-check.sh

build: check
