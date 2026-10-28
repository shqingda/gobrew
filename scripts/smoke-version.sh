#!/bin/bash
# Real historical-version upgrade test. Evidence survives; all installed state is disposable.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd -P)
binary=${1:-"$repo/bin/gobrew"}
if [[ ! -x "$binary" ]]; then
  echo 'Build gobrew first, or pass its executable path.' >&2
  exit 1
fi
binary=$(cd "$(dirname "$binary")" && pwd -P)/$(basename "$binary")
if [[ $(uname -s) != Darwin || $(uname -m) != arm64 || $(id -u) == 0 ]]; then
  echo 'Version smoke requires Apple Silicon macOS and a non-root user.' >&2
  exit 1
fi
# An explicit output directory must be new so a failed rerun cannot overwrite evidence.
if [[ $# -ge 2 ]]; then
  mkdir "$2"
  evidence=$(cd "$2" && pwd -P)
else
  evidence=$(mktemp -d /tmp/gobrew-version-evidence.XXXXXX)
fi
prefix= work= stage=setup
finish() {
  local code=$?
  trap - EXIT
  printf 'exit_code=%s\nlast_stage=%s\n' "$code" "$stage" > "$evidence/result.txt"
  [[ -z "$prefix" ]] || rm -rf -- "$prefix"
  [[ -z "$work" ]] || rm -rf -- "$work"
  printf 'Version smoke exit %s; evidence: %s\n' "$code" "$evidence" >&2
  exit "$code"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
prefix=$(mktemp -d /tmp/gv.XXXX)
work=$(mktemp -d /tmp/gobrew-version-work.XXXXXX)
mkdir "$work/home" "$work/cache" "$work/config" "$work/tmp"

# Do not inherit cleanup/pinning switches or search the developer's Homebrew PATH.
# GitHub API credentials remain inherited and are never written to evidence.
while IFS= read -r variable; do
  case "$variable" in GOBREW_*) unset "$variable" ;; esac
done < <(compgen -e)
export HOME="$work/home" XDG_CONFIG_HOME="$work/config" XDG_CACHE_HOME="$work/cache"
export TMPDIR="$work/tmp/" ZDOTDIR="$work/home" SHELL=/bin/zsh
export PATH=/usr/bin:/bin:/usr/sbin:/sbin HOMEBREW_PREFIX="$work/homebrew"
export GOBREW_BIN_DIR="$work/commands" GOBREW_NO_MODIFY_PATH=1 GOBREW_NO_AUTO_UPDATE=1
printf '%s\n' '# isolated user shell settings' > "$HOME/.zshrc"
gb=("$binary" --prefix "$prefix" --cache-dir "$work/cache" --json)
old_version=0.13.5
binary_sha=$(shasum -a 256 "$binary" | awk '{print $1}')
{
  printf 'started_utc=%s\n' "$(date -u +%FT%TZ)"
  printf 'source_commit=%s\n' "$(git -C "$repo" rev-parse HEAD)"
  printf 'binary=%s\nprefix=%s\nwork=%s\nfixture=uv %s\n' "$binary" "$prefix" "$work" "$old_version"
  printf 'os=%s\narchitecture=%s\n' "$(sw_vers -productVersion)" "$(uname -m)"
  printf 'binary_sha256=%s\n' "$binary_sha"
  printf 'auto_update=disabled\nauto_cleanup=enabled\npath_modification=disabled\n'
} > "$evidence/manifest.txt"
git -C "$repo" status --short > "$evidence/source-status.txt"
cp "$repo/scripts/smoke-version.sh" "$evidence/script.sh"

fail() { printf '%s\n' "$*" | tee -a "$evidence/assertions.txt" >&2; exit 1; }
field() { /usr/bin/plutil -extract "$2" raw -o - "$evidence/$1.stdout"; }
equal() { [[ "$1" == "$2" ]] || fail "$3: expected '$1', got '$2'"; }
run() {
  local expected=$1 actual
  stage=$2
  shift 2
  { printf '%s' "$stage"; printf '\t%q' "$@"; printf '\n'; } >> "$evidence/commands.txt"
  if "$@" > "$evidence/$stage.stdout" 2> "$evidence/$stage.stderr"; then
    actual=0
  else
    actual=$?
  fi
  printf '%s\t%s\n' "$stage" "$actual" >> "$evidence/exits.tsv"
  if [[ "$actual" != "$expected" ]]; then
    cat "$evidence/$stage.stderr" >&2
    cat "$evidence/$stage.stdout" >&2
    fail "$stage: expected exit $expected, got $actual"
  fi
}
gobrew() {
  local expected=$1 label=$2
  shift 2
  run "$expected" "$label" "${gb[@]}" "$@"
  equal 1 "$(field "$label" schema_version)" "$label JSON envelope"
}
record() {
  local label=$1 version=$2 pinned=$3
  gobrew 0 "$label" list --installed-on-request
  equal 1 "$(field "$label" data)" 'requested package count'
  equal uv "$(field "$label" data.0.formula.name)" 'requested package name'
  equal "$version" "$(field "$label" data.0.formula.version)" 'installed version'
  equal "$pinned" "$(field "$label" data.0.pinned)" 'pin state'
  equal ready "$(field "$label" data.0.status)" 'installation status'
  equal true "$(field "$label" data.0.linked)" 'link state'
}
preserved() {
  equal 'configuration sentinel' "$(cat "$prefix/etc/version-smoke.conf")" 'configuration retention'
  equal 'foreign keg sentinel' "$(cat "$prefix/Cellar/uv/0.0.0/foreign.txt")" 'foreign keg retention'
  equal '# isolated user shell settings' "$(cat "$HOME/.zshrc")" 'shell configuration retention'
}

gobrew 0 build --version
equal dev "$(field build data.version)" 'use an unversioned source build to avoid self-update'
gobrew 0 catalog update
gobrew 0 available info uv
current_version=$(field available data.0.version)
current_revision=$(field available data.0.revision)
if ! awk -v current="$current_version" -v old="$old_version" 'BEGIN {
  if (current !~ /^[0-9]+\.[0-9]+\.[0-9]+$/) exit 1
  split(current, a, "."); split(old, b, ".")
  for (i=1; i<=3; i++) { if (a[i]+0 > b[i]+0) exit 0; if (a[i]+0 < b[i]+0) exit 1 }
  exit 1
}'; then
  fail "Fixture must be older than catalog uv; fixture=$old_version catalog=$current_version"
fi
cp "$work/cache/catalog/CURRENT" "$evidence/catalog-generation.txt"
printf 'catalog_version=%s\ncatalog_revision=%s\n' "$current_version" "$current_revision" >> "$evidence/manifest.txt"
[[ ! -e "$prefix/.gobrew/state.db" ]] || fail 'Catalog queries initialized the Formula prefix'

gobrew 0 historical version-install uv "$old_version"
[[ -f "$prefix/.gobrew/state.db" ]] || fail 'First version-install did not initialize state'
equal uv "$(field historical data.pinned.0)" 'historical result pin'
record historical-record "$old_version" true
old_keg=$(field historical-record data.0.keg)
run 0 historical-executable "$prefix/bin/uv" --version
case "$(cat "$evidence/historical-executable.stdout")" in
  "uv $old_version"|"uv $old_version "*) ;;
  *) fail 'Historical executable reports the wrong version' ;;
esac

mkdir -p "$prefix/etc" "$prefix/Cellar/uv/0.0.0"
printf '%s\n' 'configuration sentinel' > "$prefix/etc/version-smoke.conf"
printf '%s\n' 'foreign keg sentinel' > "$prefix/Cellar/uv/0.0.0/foreign.txt"
gobrew 1 pinned-install install uv
case "$(field pinned-install error.message)" in *'gob unpin uv'*) ;; *) fail 'Pinned install omitted unpin guidance' ;; esac
record pinned-record "$old_version" true
gobrew 0 unpin unpin uv
export GOBREW_NO_INSTALL_UPGRADE=1
gobrew 1 disabled-upgrade install uv
record disabled-record "$old_version" false
unset GOBREW_NO_INSTALL_UPGRADE

gobrew 0 upgraded install uv
record upgraded-record "$current_version" false
equal "$current_revision" "$(field upgraded-record data.0.formula.revision)" 'upgraded revision'
new_keg=$(field upgraded-record data.0.keg)
[[ "$new_keg" != "$old_keg" && -d "$new_keg" ]] || fail 'Upgrade did not switch the keg'
[[ ! -e "$old_keg" ]] || fail 'Install did not automatically clean the old keg'
[[ -L "$prefix/bin/uv" && -L "$prefix/opt/uv" ]] || fail 'Upgrade omitted executable or opt link'
run 0 upgraded-executable "$prefix/bin/uv" --version
case "$(cat "$evidence/upgraded-executable.stdout")" in
  "uv $current_version"|"uv $current_version "*) ;;
  *) fail 'Upgraded executable reports the wrong version' ;;
esac
preserved

gobrew 0 noop install uv
equal uv "$(field noop data.unchanged.0)" 'no-op result'
equal 0 "$(field noop data.timings_ms.prepare)" 'no-op Prepare time'
equal 0 "$(field noop data.timings_ms.apply)" 'no-op Apply time'
record noop-record "$current_version" false
equal "$new_keg" "$(field noop-record data.0.keg)" 'no-op keg'
cmp "$evidence/catalog-generation.txt" "$work/cache/catalog/CURRENT"
gobrew 0 doctor doctor
gobrew 0 uninstall uninstall uv
gobrew 0 autoremove autoremove
gobrew 0 cleanup cleanup
gobrew 0 empty list
equal 0 "$(field empty data)" 'remaining package count'
[[ ! -e "$prefix/bin/uv" && ! -L "$prefix/bin/uv" ]] || fail 'Uninstall left the public executable'
preserved
equal "$binary_sha" "$(shasum -a 256 "$binary" | awk '{print $1}')" 'CLI binary retention'
stage=complete
printf '%s\n' 'Historical install, pin, upgrade, automatic cleanup and no-op passed.' | tee "$evidence/assertions.txt"
