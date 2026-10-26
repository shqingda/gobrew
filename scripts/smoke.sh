#!/bin/bash
set -euo pipefail

# Only a newly created short prefix and cache are mutated. /tmp/gb.XXXX is
# twelve bytes, within Homebrew's thirteen-byte arm64 relocation budget.
binary=${1:-"$(pwd)/bin/gobrew"}
if [[ ! -x "$binary" ]]; then
  echo "Build gobrew first, or pass its executable path." >&2
  exit 1
fi
if [[ $(uname -s) != Darwin || $(uname -m) != arm64 ]]; then
  echo "Real-bottle smoke tests require Apple Silicon macOS." >&2
  exit 1
fi
prefix=$(mktemp -d /tmp/gb.XXXX)
cache=$(mktemp -d "${TMPDIR:-/tmp}/gobrew-smoke-cache.XXXXXX")
trap 'rm -rf -- "$prefix" "$cache"' EXIT
gb=("$binary" --prefix "$prefix" --cache-dir "$cache")

"${gb[@]}" init
"${gb[@]}" update
"${gb[@]}" install hello jq ca-certificates node python@3.15
"$prefix/bin/hello"
test "$(printf '%s' '{"answer":42}' | "$prefix/bin/jq" -r .answer)" = 42
test -s "$prefix/etc/ca-certificates/cert.pem"
# Exercise the relocated dynamic-library closure, including ICU and OpenSSL,
# and the npm executables created by declarative post-install actions.
"$prefix/bin/node" -e 'require("node:crypto").randomBytes(8); console.log(new Intl.DateTimeFormat("en-US").format(0))'
PATH="$prefix/bin:/usr/bin:/bin:/usr/sbin:/sbin" "$prefix/bin/npm" --version
python="$prefix/opt/python@3.15/bin/python3.15"
"$python" -c 'import ssl, sqlite3; assert ssl.create_default_context().cert_store_stats()["x509_ca"] > 0; assert sqlite3.connect(":memory:").execute("select 42").fetchone()[0] == 42'
"$python" -m venv "$prefix/.smoke-venv"
"$prefix/.smoke-venv/bin/python" -m pip --version
"${gb[@]}" --json list
"${gb[@]}" install hello jq ca-certificates node python@3.15
"${gb[@]}" pin jq
"${gb[@]}" unpin jq
"${gb[@]}" unlink hello
test ! -e "$prefix/bin/hello"
"${gb[@]}" link hello
"$prefix/bin/hello"
"${gb[@]}" reinstall jq
"$prefix/bin/jq" --version
"${gb[@]}" doctor
"${gb[@]}" uninstall hello jq node python@3.15
test ! -e "$prefix/bin/hello"
test ! -e "$prefix/bin/jq"
"${gb[@]}" autoremove
"${gb[@]}" cleanup
echo 'Real-bottle smoke tests passed.'
