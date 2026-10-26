# Engineering constraints

- Go 1.27.3; macOS ARM64 15+ is the supported installation platform.
- Keep the core independent of CLI, GUI, process exit and terminal streams.
- No Homebrew/Ruby fallback, unknown installation semantics, or ignored integrity errors.
- Never weaken filesystem confinement, signature checks, transaction durability or tests to improve benchmarks.
- Test high-loss boundaries and observable behavior. No coverage quota or tests of trivial forwarding and data containers.
- Do not alter existing Homebrew installations or user configuration during development or benchmarks.
- Keep dependencies pinned and explain additions; use the standard library when it is sufficient.
- Required verification: formatting, go vet, go test -race, CGO_ENABLED=0 build, govulncheck and documented real-bottle smoke tests.
- Report unsupported capabilities and unverified release gates truthfully.
