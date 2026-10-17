#!/bin/sh
# Get afk on a new machine:
#
#   curl -fsSL https://raw.githubusercontent.com/dphaener/afk/main/install.sh | sh
#
# Downloads the latest afk release for this machine, checks it against the
# release's checksums.txt, puts it at ~/bin/afk and starts `afk install`, which
# sets up everything else. It never uses sudo and writes nowhere but ~/bin and a
# temporary folder it removes again.
#
# Environment:
#   AFK_VERSION=1.2.3   install this release instead of the latest (a leading
#                       "v" is fine)
#   AFK_NO_INSTALL=1    stop once ~/bin/afk is in place; do not run afk install
#
# POSIX sh only: on Debian and Ubuntu `sh` is dash, and a fresh machine may have
# nothing else.
#
# Everything lives in functions and the only top-level command is `main "$@"` on
# the last line. When the script arrives through a pipe the shell runs it as it
# reads it, so a download cut short half way would otherwise run the first half;
# with the wrapper, a cut-short script defines a few functions and does nothing.

set -u

repo_url="https://github.com/dphaener/afk"

# Set by main and read by the cleanup trap, which cannot take arguments.
tmp_dir=""
new_file=""

say() {
	printf 'afk-install: %s\n' "$*"
}

err() {
	printf 'afk-install: %s\n' "$*" >&2
}

die() {
	err "$@"
	exit 1
}

# shellcheck disable=SC2317 # only called by the EXIT trap, which shellcheck cannot see
cleanup() {
	if [ -n "$new_file" ]; then
		rm -f "$new_file"
	fi
	if [ -n "$tmp_dir" ]; then
		rm -rf "$tmp_dir"
	fi
}

refuse_root() {
	uid=$(id -u)
	if [ "$uid" = "0" ]; then
		err "you are running this as root, and afk runs as a normal user."
		err "Make a user with 'adduser <name>', log in as that user, and run this again."
		err "Nothing was installed."
		exit 1
	fi
}

unsupported() {
	err "$1"
	err "afk has ready-made downloads for Linux and macOS, on x86_64 (amd64) and arm64."
	err "On anything else you can build it from source; see docs/install.md in $repo_url"
	exit 1
}

# Sets os and arch to the names the release archives use.
detect_platform() {
	kernel=$(uname -s)
	machine=$(uname -m)
	case "$kernel" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) unsupported "there is no afk download for this operating system ($kernel)." ;;
	esac
	case "$machine" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) unsupported "there is no afk download for this processor ($machine)." ;;
	esac
}

# Sets version (no leading "v").
#
# The latest release is found by following the redirect of /releases/latest to
# /releases/tag/<tag> rather than asking the GitHub API: the API allows only 60
# unauthenticated calls an hour per address (shared machines and CI runners hit
# that), and its JSON answer would need jq, which a fresh machine rarely has.
find_version() {
	if [ -n "${AFK_VERSION:-}" ]; then
		version=${AFK_VERSION#v}
	else
		final_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$repo_url/releases/latest" </dev/null) || final_url=""
		case "$final_url" in
		*/releases/tag/*) version=${final_url##*/releases/tag/} ;;
		*)
			err "could not find the latest afk release at $repo_url/releases."
			err "Either there is no release yet or this machine cannot reach GitHub."
			err "Check your connection and try again, or set AFK_VERSION to a version you know exists."
			exit 1
			;;
		esac
		version=${version#v}
	fi
	# The version goes into a URL and a file name, so anything that is not a
	# plain version is refused rather than passed along.
	case "$version" in
	'' | *[!0-9A-Za-z.+-]*) die "'$version' does not look like an afk version (for example 1.2.3)." ;;
	esac
}

download() {
	if ! curl -fsSL -o "$2" "$1" </dev/null; then
		err "could not download $1"
		err "Check that version $version exists at $repo_url/releases and that this machine can reach GitHub."
		exit 1
	fi
}

# Prints the SHA-256 of $1, or nothing when no hashing tool is found.
# Linux ships sha256sum; macOS ships shasum instead.
file_hash() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

# Stops unless the archive's hash matches its line in checksums.txt. Runs
# before anything is extracted, so a damaged or tampered download never gets
# near ~/bin.
verify_checksum() {
	# Field 2 is the file name; sha256sum writes "*name" in binary mode.
	want=$(awk -v f="$archive" '{ n = $2; sub(/^\*/, "", n); if (n == f) { print $1; exit } }' "$tmp_dir/checksums.txt")
	if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
		die "this machine has neither sha256sum nor shasum, so the download cannot be checked. Nothing was installed."
	fi
	got=$(file_hash "$tmp_dir/$archive")
	if [ -z "$want" ] || [ "$want" != "$got" ]; then
		err "the checksum did not match, nothing was installed."
		err "The download of $archive may be damaged. Run this again; if it keeps happening, report it at $repo_url/issues."
		exit 1
	fi
}

# Copies afk to a temporary name inside ~/bin and renames it into place. The
# rename is within one folder, so it is atomic (a running old afk keeps its file)
# and ~/bin/afk ends up a new file rather than old bytes overwritten in place:
# macOS caches code-signing information per file, and a signed binary whose
# bytes are rewritten in place can be killed on its next run (see the install
# notes in CLAUDE.md about binaries killed with exit 137).
place_binary() {
	mkdir -p "$tmp_dir/x"
	if ! tar -xzf "$tmp_dir/$archive" -C "$tmp_dir/x" </dev/null; then
		die "could not unpack $archive. Nothing was installed."
	fi
	if [ ! -f "$tmp_dir/x/afk" ]; then
		die "$archive holds no afk program. Nothing was installed."
	fi
	mkdir -p "$HOME/bin" || die "could not make the folder $HOME/bin."
	new_file="$HOME/bin/.afk.new.$$"
	cp "$tmp_dir/x/afk" "$new_file" || die "could not write to $HOME/bin."
	chmod 755 "$new_file" || die "could not make $new_file runnable."
	mv -f "$new_file" "$HOME/bin/afk" || die "could not put afk at $HOME/bin/afk."
	new_file=""
}

# Sets version_line to what `afk version` printed; fails if it did not run.
# stdin is /dev/null so afk cannot read the rest of a piped script.
run_version() {
	version_line=$("$HOME/bin/afk" version </dev/null 2>&1)
}

# Tries the binary first and signs only if it does not run. Whether a
# downloaded macOS binary needs an ad-hoc `codesign` was not settled when the
# releases were set up, so trying first works either way, and a binary that
# already runs is never re-signed.
check_runs() {
	if run_version; then
		return 0
	fi
	if [ "$os" = darwin ] && command -v codesign >/dev/null 2>&1; then
		codesign --force --sign - "$HOME/bin/afk" </dev/null >/dev/null 2>&1
		if run_version; then
			return 0
		fi
	fi
	err "afk was put at $HOME/bin/afk but does not run."
	if [ -n "$version_line" ]; then
		err "It said: $version_line"
	fi
	if [ "$os" = darwin ]; then
		err "Try signing it by hand, then run '$HOME/bin/afk version':"
		err "  codesign --force --sign - \"$HOME/bin/afk\""
	else
		err "Check that this machine is $os on $arch, and report it at $repo_url/issues if it is."
	fi
	exit 1
}

path_hint() {
	case ":$PATH:" in
	*":$HOME/bin:"*) return 0 ;;
	esac
	if [ "$os" = darwin ]; then
		rc=.zshrc
	else
		rc=.bashrc
	fi
	say "$HOME/bin is not on your PATH yet, so typing 'afk' will not find it."
	say "Add this line to ~/$rc, then open a new terminal:"
	say "  export PATH=\"\$HOME/bin:\$PATH\""
}

# Runs `afk install` with its input from the terminal. When this script came
# through `curl | sh`, stdin is the pipe holding the script, so afk install
# could not ask its questions; /dev/tty is the terminal the person typed into.
# AFK_TTY is a test seam: the shell test points it at a file so it can check
# where afk install's input came from. Nothing else should set it.
start_installer() {
	tty=${AFK_TTY:-/dev/tty}
	# The open is tried in a subshell: a failed redirection on a special
	# builtin ends a non-interactive POSIX shell, and with no controlling
	# terminal (CI, cron) opening /dev/tty fails.
	if ! (exec <"$tty") 2>/dev/null; then
		say "afk is in place, but there is no terminal here to answer its questions."
		say "Run '$HOME/bin/afk install' from a terminal to finish setting up."
		exit 0
	fi
	say "starting afk install"
	"$HOME/bin/afk" install <"$tty"
	exit $?
}

main() {
	refuse_root
	detect_platform
	find_version

	trap cleanup EXIT
	# dash does not run the EXIT trap when a signal ends the shell, so the
	# signals exit explicitly, and that exit runs it.
	trap 'exit 1' INT TERM
	tmp_dir=$(mktemp -d) || die "could not make a temporary folder."

	archive="afk_${version}_${os}_${arch}.tar.gz"
	base="$repo_url/releases/download/v$version"
	say "downloading afk $version for $os/$arch"
	download "$base/$archive" "$tmp_dir/$archive"
	download "$base/checksums.txt" "$tmp_dir/checksums.txt"
	verify_checksum
	place_binary
	check_runs
	say "installed $HOME/bin/afk ($version_line)"
	path_hint

	if [ "${AFK_NO_INSTALL:-}" = "1" ]; then
		say "afk is in place. The next step is '$HOME/bin/afk install', which sets up everything else."
		exit 0
	fi
	start_installer
}

main "$@"
