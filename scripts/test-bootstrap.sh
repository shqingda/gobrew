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
mkdir "$work/home"
printf '%s\n' '# user settings' > "$work/home/.zshrc"
export PATH="$work/mock:$PATH" BOOTSTRAP_FIXTURES="$work/assets" GOBREW_BIN_DIR="$work/bin" GOBREW_PREFIX="$work/prefix"
export HOME="$work/home" SHELL=/bin/zsh
unset GOBREW_VERSION ZDOTDIR GOBREW_NO_MODIFY_PATH
sh "$repo/install.sh"
test "$(readlink "$work/bin/gob")" = gobrew
# Formula storage is left to the first install, which needs no separate init.
test ! -e "$work/prefix"
# New shells find the commands without manual PATH edits.
test "$(zsh -c 'source "$HOME/.zshrc"; whence -p gob')" = "$work/bin/gob"
"$work/bin/gob" --version
"$work/bin/gobrew" --version
sh "$repo/install.sh"
# Verification failure must leave both the executable and receipt untouched.
before=$(shasum -a 256 "$work/bin/.gobrew/receipt.json")
printf 'corruption' >> "$work/assets/gobrew_0.2.0_darwin_arm64.tar.gz"
if sh "$repo/install.sh"; then
    printf '%s\n' 'bootstrap accepted a corrupted archive' >&2
    exit 1
fi
test "$before" = "$(shasum -a 256 "$work/bin/.gobrew/receipt.json")"
test "$(grep -c '>>> gobrew >>>' "$work/home/.zshrc")" = 1
# A foreign gob stops the uninstaller before anything changes.
mv "$work/bin/gob" "$work/gob.saved"
printf 'foreign\n' > "$work/bin/gob"
if sh "$repo/uninstall.sh"; then
    printf '%s\n' 'uninstall removed a foreign gob' >&2
    exit 1
fi
test -L "$work/bin/gobrew" && test -f "$work/bin/.gobrew/receipt.json"
rm "$work/bin/gob" && mv "$work/gob.saved" "$work/bin/gob"
printf '%s\n' 'export EDITOR=vim' >> "$work/home/.zshrc"
sh "$repo/uninstall.sh"
test "$(cat "$work/home/.zshrc")" = "$(printf '%s\n%s' '# user settings' 'export EDITOR=vim')"
test ! -e "$work/bin/gobrew" && test ! -L "$work/bin/gobrew"
test ! -e "$work/bin/gob" && test ! -L "$work/bin/gob"
test ! -e "$work/bin/.gobrew"
# The CLI installs again after an uninstall.
(cd "$work/assets" && tar -czf gobrew_0.2.0_darwin_arm64.tar.gz gobrew_0.2.0_darwin_arm64 && shasum -a 256 gobrew_0.2.0_darwin_arm64.tar.gz > checksums.txt)
sh "$repo/install.sh"
test "$(readlink "$work/bin/gob")" = gobrew
# gobrew self covers the same lifecycle from inside the executable, keeping
# only the small records it needs for retries, and uninstall.sh still finishes.
"$work/bin/gob" self uninstall
test "$(cat "$work/home/.zshrc")" = "$(printf '%s\n%s' '# user settings' 'export EDITOR=vim')"
test ! -e "$work/bin/gobrew" && test ! -L "$work/bin/gobrew"
test ! -e "$work/bin/gob" && test ! -L "$work/bin/gob"
test -f "$work/bin/.gobrew/receipt.json"
"$work/assets/gobrew_0.2.0_darwin_arm64/gobrew" self install --bin-dir "$work/bin"
test "$(readlink "$work/bin/gob")" = gobrew
test "$(grep -c '>>> gobrew >>>' "$work/home/.zshrc")" = 1
"$work/bin/gobrew" self uninstall
sh "$repo/uninstall.sh"
test ! -e "$work/bin/.gobrew"
printf '%s\n' 'Bootstrap, alias, repeat installation, integrity rejection, uninstall, reinstall and self lifecycle passed.'
