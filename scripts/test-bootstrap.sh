#!/bin/sh
# Offline end-to-end bootstrap test. All commands and ownership live in a temp root.
set -eu
binary=$(cd "$(dirname "$1")" && pwd -P)/$(basename "$1")
repo=$(cd "$(dirname "$0")/.." && pwd -P)
work=$(mktemp -d /tmp/gobrew-bootstrap-test.XXXXXX)
work=$(cd "$work" && pwd -P)
trap 'rm -r "$work"' EXIT
mkdir -p "$work/mock" "$work/assets/gobrew_0.2.0_darwin_arm64"
cp "$binary" "$work/assets/gobrew_0.2.0_darwin_arm64/gobrew"
(cd "$work/assets" && tar -czf gobrew_0.2.0_darwin_arm64.tar.gz gobrew_0.2.0_darwin_arm64 && shasum -a 256 gobrew_0.2.0_darwin_arm64.tar.gz > checksums.txt)
cat > "$work/mock/curl" <<'CURL'
#!/bin/sh
set -eu
output= url=
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o) output=$2; shift ;;
        https://*) url=$1 ;;
    esac
    shift
done
case "$url" in
    https://github.com/shqingda/gobrew/releases/latest)
        printf '%s' https://github.com/shqingda/gobrew/releases/tag/v0.2.0 ;;
    https://github.com/shqingda/gobrew/releases/download/v0.2.0/*)
        cp "$BOOTSTRAP_FIXTURES/${url##*/}" "$output" ;;
    *) exit 99 ;;
esac
CURL
chmod +x "$work/mock/curl"
export PATH="$work/mock:$PATH" BOOTSTRAP_FIXTURES="$work/assets" GOBREW_BIN_DIR="$work/bin" GOBREW_PREFIX="$work/prefix"
unset GOBREW_VERSION
sh "$repo/install.sh"
test "$(readlink "$work/bin/gob")" = gobrew
# The bootstrap also initializes Formula storage, so no separate init is needed.
test -f "$work/prefix/.gobrew/state.db"
"$work/bin/gob" version
"$work/bin/gobrew" version
sh "$repo/install.sh"
# Storage that cannot be created must not fail the CLI installation; the
# temporary prefix is never the system prefix, so no sudo is attempted.
mkdir -m 0555 "$work/locked"
GOBREW_PREFIX="$work/locked/prefix" sh "$repo/install.sh" 2> "$work/locked.err"
grep -q 'first gob install will retry' "$work/locked.err"
chmod 0755 "$work/locked"
# Verification failure must leave both the executable and receipt untouched.
before=$(shasum -a 256 "$work/bin/.gobrew/receipt.json")
printf 'corruption' >> "$work/assets/gobrew_0.2.0_darwin_arm64.tar.gz"
if sh "$repo/install.sh"; then
    printf '%s\n' 'bootstrap accepted a corrupted archive' >&2
    exit 1
fi
test "$before" = "$(shasum -a 256 "$work/bin/.gobrew/receipt.json")"
"$work/bin/gob" self uninstall
test ! -e "$work/bin/gobrew" && test ! -L "$work/bin/gobrew"
test ! -e "$work/bin/gob" && test ! -L "$work/bin/gob"
printf '%s\n' 'Bootstrap, storage setup, alias, repeat installation, integrity rejection and self-uninstall passed.'
