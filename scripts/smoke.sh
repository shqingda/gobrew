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
# Keep the run deterministic, as the benchmark does.
export GOBREW_NO_AUTO_UPDATE=1

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
# Query output follows brew, over a real catalog and real kegs.
"${gb[@]}" deps --tree jq | grep -x '└── oniguruma' >/dev/null
"${gb[@]}" deps --installed | grep -x 'jq: oniguruma' >/dev/null
"${gb[@]}" list jq | grep -x "$prefix/Cellar/jq/.*/bin/jq" >/dev/null
"${gb[@]}" info jq | grep -x '==> Installed Versions' >/dev/null
"${gb[@]}" info | grep -E '^[0-9]+ kegs, ' >/dev/null
"${gb[@]}" install hello jq ca-certificates node python@3.15
"${gb[@]}" pin jq
"${gb[@]}" unpin jq
# python@3.15 brings in keg-only sqlite: link only warns without --force.
"${gb[@]}" link sqlite
test ! -e "$prefix/bin/sqlite3"
"${gb[@]}" link --dry-run sqlite | grep -x "$prefix/bin/sqlite3" >/dev/null
"${gb[@]}" link --force sqlite
"$prefix/bin/sqlite3" --version
"${gb[@]}" unlink sqlite
"${gb[@]}" unlink --dry-run hello | grep -x "$prefix/bin/hello" >/dev/null
test -e "$prefix/bin/hello"
"${gb[@]}" unlink hello
test ! -e "$prefix/bin/hello"
"${gb[@]}" link --dry-run hello | grep -x "$prefix/bin/hello" >/dev/null
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
# Only a broken link into this Cellar is gobrew's to prune.
ln -s "$prefix/Cellar/gone/1/bin/gone" "$prefix/bin/gone"
ln -s /nonexistent/foreign "$prefix/bin/foreign"
"${gb[@]}" cleanup --prune-prefix --dry-run | grep -x "Would remove (broken link): $prefix/bin/gone" >/dev/null
test -L "$prefix/bin/gone"
"${gb[@]}" cleanup --prune-prefix
test ! -L "$prefix/bin/gone"
test -L "$prefix/bin/foreign"
rm "$prefix/bin/foreign"
echo 'Real-bottle smoke tests passed.'
