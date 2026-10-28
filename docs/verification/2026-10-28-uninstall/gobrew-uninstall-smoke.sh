#!/bin/bash
set -euo pipefail
binary=/Users/shqingda/Projects/gobrew/bin/gobrew
prefix=$(mktemp -d /tmp/gb.XXXX)
cache=$(mktemp -d /tmp/gobrew-uninstall-cache.XXXXXX)
test_home=$(mktemp -d /tmp/gobrew-uninstall-home.XXXXXX)
trap 'rm -rf -- "$prefix" "$cache" "$test_home"' EXIT
export HOME="$test_home" GOBREW_NO_AUTO_UPDATE=1 GOBREW_NO_MODIFY_PATH=1
gb=("$binary" --prefix "$prefix" --cache-dir "$cache")
"${gb[@]}" install jq
"${gb[@]}" list
test "$(printf '{"answer":42}' | "$prefix/bin/jq" -r .answer)" = 42
if "${gb[@]}" uninstall oniguruma; then
  echo 'ERROR: removed a required dependency' >&2
  exit 1
fi
"${gb[@]}" uninstall jq
"${gb[@]}" list
test ! -e "$prefix/bin/jq"
test ! -e "$prefix/opt/oniguruma"
test -z "$(find "$prefix/Cellar" -name .gobrew-owner -print)"
"${gb[@]}" install jq oniguruma
"${gb[@]}" uninstall jq
"${gb[@]}" list
test -d "$prefix/opt/oniguruma"
"${gb[@]}" uninstall oniguruma
"${gb[@]}" list
echo 'Isolated jq dependency uninstall smoke passed.'
