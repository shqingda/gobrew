# Engineering constraints

- Go 1.27.3; macOS ARM64 15+ is the supported installation platform.
- Keep the core independent of CLI, GUI, process exit and terminal streams.
- No Homebrew/Ruby fallback, unknown installation semantics, or ignored integrity errors.
- Bottle metadata must come from Homebrew's signed API snapshot. The single exception is an explicit `version-install` request for a version the snapshot no longer contains: its bottle is accepted only when the GHCR OCI index and the homebrew-core "update bottle" commit record the same digest for the host tag, the recipe defines no Ruby post-install hook or pour condition, and the installed formula is pinned. The user is told that the version is outside the signed metadata. No other path may use unsigned metadata.
- Never weaken filesystem confinement, signature checks, transaction durability or tests to improve benchmarks.
- Test high-loss boundaries and observable behavior. No coverage quota or tests of trivial forwarding and data containers.
- Do not alter existing Homebrew installations or user configuration during development or benchmarks.
- Keep dependencies pinned and explain additions; use the standard library when it is sufficient.
- Required verification: formatting, go vet, go test -race, CGO_ENABLED=0 build, govulncheck and documented real-bottle smoke tests.
- Report unsupported capabilities and unverified release gates truthfully.
