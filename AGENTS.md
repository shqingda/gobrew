# Repository guidance

Read CONSTRAINTS.md and docs/architecture.md before changing installation behavior.
Keep module boundaries, immutable prepared artifacts and the Plan/Prepare/Apply contract.
Tests must use temporary prefixes; never mutate the developer's Homebrew installation.
Use explicit ownership and durable journals for filesystem changes. Do not add generic frameworks.
