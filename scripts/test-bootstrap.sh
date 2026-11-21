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
# zsh completes gobrew and gob whether compinit runs before or after the block.
for f in _gobrew gobrew.bash gobrew.fish; do test -s "$work/bin/.gobrew/completions/$f"; done
test "$(zsh -fc 'source "$HOME/.zshrc"; autoload -Uz compinit; compinit -u -d "$HOME/.zcd1"; print -r -- ${_comps[gob]} ${_comps[gobrew]}')" = "_gobrew _gobrew"
test "$(zsh -fc 'autoload -Uz compinit; compinit -u -d "$HOME/.zcd2"; source "$HOME/.zshrc"; print -r -- ${_comps[gob]} ${_comps[gobrew]}')" = "_gobrew _gobrew"
"$work/bin/gob" __complete ins | grep -q '^install'
rm -f "$work/home/.zcd1" "$work/home/.zcd2"
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
test ! -e "$work/bin/.gobrew/completions"
"$work/assets/gobrew_0.2.0_darwin_arm64/gobrew" self install --bin-dir "$work/bin"
test -s "$work/bin/.gobrew/completions/_gobrew"
test "$(readlink "$work/bin/gob")" = gobrew
test "$(grep -c '>>> gobrew >>>' "$work/home/.zshrc")" = 1
"$work/bin/gobrew" self uninstall
sh "$repo/uninstall.sh"
test ! -e "$work/bin/.gobrew"

# Each shell writes its own startup file, and uninstall.sh removes only that.
branch() {
    rm -rf "$work/home" "$work/bin"
    mkdir "$work/home"
    export HOME="$work/home" SHELL="$1"
}
# bash: an existing .profile is used, never shadowed by a new .bash_profile.
branch /bin/bash
printf '%s\n' '# profile' > "$work/home/.profile"
sh "$repo/install.sh"
test ! -e "$work/home/.bash_profile" && test ! -e "$work/home/.bash_login"
test "$(grep -c '>>> gobrew >>>' "$work/home/.profile")" = 1
test "$(env -i HOME="$HOME" PATH=/usr/bin:/bin /bin/bash -lc 'command -v gob')" = "$work/bin/gob"
sh "$repo/uninstall.sh"
test "$(cat "$work/home/.profile")" = '# profile'
# bash without any startup file creates .bash_profile.
branch /bin/bash
sh "$repo/install.sh"
test "$(grep -c '>>> gobrew >>>' "$work/home/.bash_profile")" = 1
sh "$repo/uninstall.sh"
test ! -s "$work/home/.bash_profile"
# fish: gobrew owns a whole conf.d file; fish need not be installed.
branch /usr/local/bin/fish
export XDG_CONFIG_HOME="$work/home/xdg"
sh "$repo/install.sh"
fish_file="$XDG_CONFIG_HOME/fish/conf.d/gobrew.fish"
test "$(head -n 1 "$fish_file")" = '# >>> gobrew >>>'
grep -q "fish_add_path\|set -gx PATH" "$fish_file"
grep -q "$work/bin" "$fish_file"
printf '%s\n' '# not gobrew' > "$work/home/xdg/fish/conf.d/other.fish"
sh "$repo/uninstall.sh"
test ! -e "$fish_file" && test -f "$work/home/xdg/fish/conf.d/other.fish"
unset XDG_CONFIG_HOME
# zsh with ZDOTDIR writes there, not to $HOME/.zshrc.
branch /bin/zsh
export ZDOTDIR="$work/home/zdot"
mkdir "$ZDOTDIR"
sh "$repo/install.sh"
test ! -e "$work/home/.zshrc"
test "$(grep -c '>>> gobrew >>>' "$ZDOTDIR/.zshrc")" = 1
sh "$repo/uninstall.sh"
test ! -s "$ZDOTDIR/.zshrc"
unset ZDOTDIR
# GOBREW_NO_MODIFY_PATH leaves every startup file alone.
branch /bin/zsh
hint=$(GOBREW_NO_MODIFY_PATH=1 sh "$repo/install.sh")
test -z "$(ls -A "$work/home")"
# The manual line names the installed command, not install.sh's temporary copy.
printf '%s\n' "$hint" | grep -qF "eval \"\$($work/bin/gobrew shellenv)\""
test "$(readlink "$work/bin/gob")" = gobrew
sh "$repo/uninstall.sh"
printf '%s\n' 'Bootstrap, alias, completions, repeat installation, integrity rejection, uninstall, reinstall, self lifecycle and shell branches passed.'
