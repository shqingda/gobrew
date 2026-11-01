#!/bin/sh
# Remove the gobrew CLI that install.sh installed, like Homebrew's uninstall
# script. Installed Formulae, configuration and caches are retained. Only the
# links, store and PATH block that gobrew wrote are removed; anything else
# stops the script before it changes a file.
# Keep execution inside main so a truncated download cannot run a partial script.
set -eu

begin='# >>> gobrew >>>'
end='# <<< gobrew <<<'

fail() {
    printf '%s\n' "$1; nothing was removed." >&2
    exit 1
}

# check_store accepts only entries the CLI store creates: the receipt, lock,
# current link, digest-named executables and interrupted atomic writes.
check_store() {
    store=$1
    [ -d "$store" ] && [ ! -L "$store" ] || fail "$store is not a gobrew store"
    for entry in "$store"/* "$store"/.[!.]*; do
        [ -e "$entry" ] || [ -L "$entry" ] || continue
        case ${entry##*/} in
            lock | receipt.json | current | link | .gobrew-write-*) ;;
            versions)
                [ -d "$entry" ] && [ ! -L "$entry" ] || fail "$entry is not a gobrew directory"
                for file in "$entry"/* "$entry"/.[!.]*; do
                    [ -e "$file" ] || [ -L "$file" ] || continue
                    name=${file##*/}
                    case $name in .gobrew-write-*) continue ;; esac
                    if ! printf '%s\n' "$name" | LC_ALL=C grep -Eq '^[0-9a-f]{64}$'; then
                        fail "unrecognized file $file"
                    fi
                done
                ;;
            *) fail "unrecognized entry $entry" ;;
        esac
    done
}

# remove_block deletes the managed block, and the blank line gobrew added
# before it, from one startup file. A damaged block is reported and kept.
remove_block() {
    file=$1
    [ -f "$file" ] || return 0
    begins=$(grep -cxF "$begin" "$file" || true)
    ends=$(grep -cxF "$end" "$file" || true)
    [ "$begins" = 0 ] && [ "$ends" = 0 ] && return 0
    if [ "$begins" != 1 ] || [ "$ends" != 1 ]; then
        printf '%s\n' "Warning: damaged gobrew block in $file; remove it by hand." >&2
        return 0
    fi
    target=$(realpath "$file")
    tmp=$(mktemp "${target%/*}/.gobrew-write-XXXXXX")
    awk -v b="$begin" -v e="$end" '
        $0 == b { inside = 1; held = 0; next }
        inside { if ($0 == e) inside = 0; next }
        held { print ""; held = 0 }
        $0 == "" { held = 1; next }
        { print }
        END { if (held) print "" }
    ' "$target" > "$tmp"
    chmod "$(stat -f %Lp "$target")" "$tmp"
    mv "$tmp" "$target"
    printf '%s\n' "Removed the gobrew PATH block from $file."
}

main() {
    bin=${GOBREW_BIN_DIR:-$HOME/.local/bin}
    store=$bin/.gobrew
    for pair in gob:gobrew gobrew:.gobrew/current; do
        path=$bin/${pair%%:*}
        [ -e "$path" ] || [ -L "$path" ] || continue
        if [ ! -L "$path" ] || [ "$(readlink "$path")" != "${pair#*:}" ]; then
            fail "$path was not installed by gobrew"
        fi
    done
    if [ -e "$store" ] || [ -L "$store" ]; then
        check_store "$store"
    fi
    rm -f "$bin/gob" "$bin/gobrew"
    rm -rf "$store"
    printf '%s\n' "Removed gobrew and gob from $bin. Installed Formulae, configuration and caches are retained."
    for file in "${ZDOTDIR:-$HOME}/.zshrc" "$HOME/.bash_profile" "$HOME/.bash_login" "$HOME/.profile"; do
        remove_block "$file"
    done
    fish=${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d/gobrew.fish
    if [ -f "$fish" ] && [ "$(head -n 1 "$fish")" = "$begin" ]; then
        rm "$fish"
        printf '%s\n' "Removed $fish."
    fi
}

main "$@"
