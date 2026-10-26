# Implementation plan

Approved scope: Go 1.27.3, macOS ARM64 15+, official core bottles, independent
`/opt/gobrew`, no Ruby/Homebrew fallback. CLI now, shared engine for future MyGo.
A separate formal Chinese Go tutorial is part of this delivery.

The system and code contracts are in `docs/architecture.md`; quality requirements
are in `CONSTRAINTS.md`. This document records execution order rather than a
second architecture specification.

1. Define shared values and Plan/Prepare/Apply contracts; verify signed catalog
   ingestion and pure dependency resolution.
2. Verify real bottles in a disposable short prefix: digest, extraction,
   relocation, signing, initialization and actual execution.
3. Complete durable filesystem activation, cache leases, failure recovery,
   shared config retention and installed dependency protection.
4. Integrate CLI lifecycle, JSON, cancellation, migration and diagnostic commands.
5. Perform independent boundary review, focused regression tests and race/vet.
6. Run five-round cold/warm/no-op measurements against isolated Homebrew with
   identical package versions and initialization. Preserve actual evidence.
7. Ship tag-driven GitHub Actions with correctness and performance gates.

Source remains single-module and CGO-free for the CLI. Runtime dependencies are
limited to Cobra, bbolt, x/sys, x/sync and their necessary transitive dependencies.
No threshold may be loosened or required initialization omitted to pass a gate.
