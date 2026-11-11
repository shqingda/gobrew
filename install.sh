#!/bin/sh
# Bootstrap only: the verified Go executable owns installation and recovery.
# Keep execution inside main so a truncated download cannot run a partial script.
set -eu

fail() {
    printf '%s\n' "$1" >&2
    exit 1
}

title() {
    if [ -t 1 ]; then
        printf '\033[34;1m==>\033[0m \033[1m%s\033[0m\n' "$1"
    else
        printf '==> %s\n' "$1"
    fi
}

main() {
    if [ "$(id -u)" -eq 0 ]; then
        printf '%s\n' 'Run this installer as your normal user, without sudo.' >&2
        exit 1
    fi
    # A shell running under Rosetta reports x86_64; the hardware is what counts.
    if [ "$(uname -s)" != Darwin ] || { [ "$(uname -m)" != arm64 ] &&
        [ "$(sysctl -n hw.optional.arm64 2>/dev/null)" != 1 ]; }; then
        fail 'gobrew requires Apple Silicon and macOS 15 or newer.'
    fi
    major=$(sw_vers -productVersion 2>/dev/null | cut -d . -f 1) || major=
    case "$major" in ''|*[!0-9]*) fail 'Could not read the macOS version from sw_vers.' ;; esac
    if [ "$major" -lt 15 ]; then
        printf '%s\n' 'gobrew requires macOS 15 or newer.' >&2
        exit 1
    fi
    work=$(mktemp -d "${TMPDIR:-/tmp}/gobrew-bootstrap.XXXXXX")
    trap 'rm -r "$work"' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM HUP
    repository=https://github.com/shqingda/gobrew
    version=${GOBREW_VERSION:-}
    if [ -z "$version" ]; then
        # Resolve latest once; all subsequent downloads bind to this exact tag.
        url=$(curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL \
            --retry 2 --connect-timeout 15 --max-time 120 -o /dev/null -w '%{url_effective}' \
            "$repository/releases/latest")
        case "$url" in
            "$repository"/releases/tag/*) version=${url##*/} ;;
            *) fail "Could not find the latest gobrew release; set GOBREW_VERSION to choose one." ;;
        esac
    fi
    if ! printf '%s\n' "$version" | LC_ALL=C grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
        printf '%s\n' 'GOBREW_VERSION must be a stable version, such as v0.2.0.' >&2
        exit 1
    fi
    archive="gobrew_${version#v}_darwin_arm64.tar.gz"
    base="$repository/releases/download/$version"
    title "Downloading gobrew $version"
    for name in checksums.txt "$archive"; do
        # Show curl's progress bar for the archive when someone is watching.
        quiet=-s
        if [ "$name" = "$archive" ] && [ -t 2 ]; then quiet=--progress-bar; fi
        curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fSL "$quiet" \
            --retry 2 --connect-timeout 15 --max-time 180 --max-filesize 33554432 \
            "$base/$name" -o "$work/$name"
    done
    expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$work/checksums.txt")
    if ! printf '%s\n' "$expected" | LC_ALL=C grep -Eq '^[0-9a-f]{64}$' || [ "${#expected}" -ne 64 ]; then
        printf '%s\n' 'Missing or ambiguous release checksum.' >&2
        exit 1
    fi
    actual=$(shasum -a 256 "$work/$archive" | awk '{print $1}')
    if [ "$actual" != "$expected" ]; then
        printf '%s\n' 'Release checksum mismatch; nothing was installed.' >&2
        exit 1
    fi
    # Stream just the executable into a private directory; never extract paths.
    # The limits match gobrew's own self-update: a 32 MiB archive (curl above)
    # and a 64 MiB executable.
    tar -xOzf "$work/$archive" "${archive%.tar.gz}/gobrew" | head -c 67108865 > "$work/gobrew" ||
        fail 'The release archive does not contain the gobrew executable.'
    test -s "$work/gobrew" || fail 'The release archive does not contain the gobrew executable.'
    size=$(wc -c < "$work/gobrew")
    [ "$size" -le 67108864 ] || fail 'The gobrew executable in the release archive exceeds 64 MiB.'
    chmod 0755 "$work/gobrew"
    bin=${GOBREW_BIN_DIR:-$HOME/.local/bin}
    title "Installing gobrew $version"
    # The verified executable installs itself and the gob alias through
    # update; the first gob install sets up Formula storage.
    GOBREW_INSTALL_BIN_DIR=$bin "$work/gobrew" update
    title "Next steps"
    printf '%s\n' '  gob install <formula>'
}

main "$@"
