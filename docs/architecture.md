# Architecture

gobrew is a native Go client for official Homebrew core bottles. The CLI is an
adapter around an in-process engine that can later be called by MyGo.

## Dependency direction

`cli → engine → catalog / resolver / artifact / installer / state / platform`

The `model` package contains shared values, errors and bounded progress events.
The resolver is pure: no network, database, terminal or macOS calls. Interfaces
belong at real I/O boundaries, not around every concrete implementation.

## Three phases

1. Plan fixes catalog generation, installed-state generation, exact bottles and
   their dependency graph. Unsupported semantics are rejected before activation.
2. Prepare downloads, verifies and materializes immutable, relocated trees.
   Neither the state database nor the prefix writer lock is held over networking.
3. Apply obtains the prefix lock, checks the plan generation, stages on the
   Cellar filesystem and commits changes through a durable journal.

Progress is best effort and nonblocking. Final outcomes are function results.
Cancellation is observed between safe filesystem transitions; it cannot abort a
half-committed link operation or replace recovery.

## Ownership and state

The default prefix is `/opt/gobrew`, owned by one non-root user. Bootstrap may
create only this fixed missing directory; it must never recursively chown an
existing installation. Normal formula execution never runs as root.

Catalog snapshots are separate from mutable installed state. bbolt handles are
short lived because even a read-only handle holds a process-level file lock.
The future GUI must not keep a database handle open between calls.

Installed state and filesystem operation intents live in the same bbolt file.
Before changing files, persist old/new paths and link ownership. Recover an
unfinished operation before another mutation. Never delete or replace a foreign
file during rollback. Keep database syncing enabled and do not put filesystem
side effects inside retryable DB.Batch callbacks.

Packages become `needs-postinstall` before external initialization, then `ready`
only after success. Interrupted external commands are never automatically
replayed. Repair is explicit. Configuration and mutable data are not rollbackable
and are retained during uninstall. Old kegs remain until safe reclamation.

## Dependency compatibility

Keep installed runtime dependency facts. Version ordering includes version_scheme
and revision; compatibility_version is an ABI hint, not a replacement for runtime
minimum versions. Dependency changes consider installed reverse dependents.
Pins, unavailable bottles and unsupported steps block the entire plan before
activation. Retaining old keg directories alone does not protect opt-linked users.

## Performance and cache identity

An already-installed install is a local no-op; upgrade is explicit. Help, version
and installed queries do not load the catalog or network. Catalog refresh builds
an immutable indexed generation and atomically publishes it.

Downloads share a Transport and have a separate concurrency limit from CPU/disk
preparation. Blob keys are SHA-256. Prepared tree keys include the blob digest,
prefix, Cellar, platform, path mappings and relocation implementation version.
Only complete trees before post-install may enter the immutable cache. Materialize
with APFS clone, falling back to copies, never hardlinks.

Default budgets: 2 GiB compressed and 8 GiB prepared, measured as logical bytes.
Eviction protects active operations and happens on cache insertion or cleanup,
not no-op reads. Cache hits do not require a database write for exact LRU.

## Trust boundary

Verify official metadata signatures and bottle digests. Confine extraction;
reject traversal, symlink escapes, special files and unsafe metadata. Relocation
and post-install have explicit supported operations. Unknown operations fail
closed. System commands use argument arrays and controlled environments.

## Validation and distribution

Unit and filesystem tests cover dependency planning, malicious archives,
cache isolation, journal recovery, ownership and post-install interruption.
Real ARM64 smoke tests cover executable and dynamic-library behavior. Cold,
warm and no-op benchmarks are distinct and do not skip initialization. Releases
are tagged GitHub Actions builds with checksums; no GUI dependency is required.
