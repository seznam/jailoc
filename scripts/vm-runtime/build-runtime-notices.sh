#!/usr/bin/env bash
# Print the licenses and notices of everything in the VM runtime that release
# builds of jailoc embed: libkrunfw, libkrun with the Rust crates built into it,
# the musl libc in libkrun's guest init, and go-microvm-runner with the Go
# modules built into it. GoReleaser appends the result to THIRD_PARTY_NOTICES.
#
# Usage: build-runtime-notices.sh GO_MICROVM_CHECKOUT OUT_DIR
#   GO_MICROVM_CHECKOUT  go-microvm checkout at the revision pinned in go.mod
#   OUT_DIR              receives vm-runtime-notices.txt
#
# Requires: go, git, cargo and cargo-about.
set -euo pipefail

src=$(cd "${1:?go-microvm checkout}" && pwd)
out=${2:?output directory}
here=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

pinned() { grep "^$1=" "$src/versions.env" | cut -d= -f2; }
libkrun=$(pinned LIBKRUN_VERSION)
libkrunfw=$(pinned LIBKRUNFW_VERSION)
rev=$(git -C "$src" rev-parse --short HEAD)
git clone --quiet --depth 1 --branch "$libkrun" https://github.com/libkrun/libkrun "$work/libkrun"
git clone --quiet --depth 1 --branch "$libkrunfw" https://github.com/libkrun/libkrunfw "$work/libkrunfw"

section() {
	printf '%s\n%s\n%s\n\n' "################################################################################" \
		"$1" "################################################################################"
}
file() {
	printf '%s\n%s\n%s\n\n' "================================================================================" \
		"$1" "--------------------------------------------------------------------------------"
	cat "$2"
	printf '\n'
}

{
	section "VM runtime: libkrunfw $libkrunfw"
	cat <<EOF
libkrunfw (https://github.com/libkrun/libkrunfw) bundles a Linux kernel,
licensed under GPL-2.0-only. The rest of libkrunfw is licensed under
LGPL-2.1-only. The complete corresponding source is published as
jailoc_<version>_vm-runtime-source.tar with every jailoc release at
https://github.com/seznam/jailoc/releases.

EOF
	file "libkrunfw (LICENSE-GPL-2.0-only)" "$work/libkrunfw/LICENSE-GPL-2.0-only"
	file "libkrunfw (LICENSE-LGPL-2.1-only)" "$work/libkrunfw/LICENSE-LGPL-2.1-only"

	section "VM runtime: libkrun $libkrun"
	cat <<EOF
libkrun (https://github.com/libkrun/libkrun) is licensed under the Apache
License 2.0. jailoc builds it with the patches in
https://github.com/seznam/jailoc/tree/main/scripts/vm-runtime/libkrun-patches.
The Rust standard library built into it is licensed under MIT OR Apache-2.0;
it is used under the Apache License 2.0. The Rust crates built into libkrun
and their licenses follow.

EOF
	file "libkrun (LICENSE)" "$work/libkrun/LICENSE"
	(cd "$work/libkrun" && cargo about generate --fail --locked \
		-c "$here/about.toml" -m src/libkrun/Cargo.toml --features "blk net" \
		"$here/about.hbs")
	printf '\n'

	section "VM runtime: musl libc (libkrun guest init)"
	cat <<EOF
libkrun embeds its guest init (init.krun), built from the libkrun source above
and statically linked against musl libc (https://musl.libc.org), licensed
under the MIT license, and the GCC runtime libraries, licensed under GPL-3.0
with the GCC Runtime Library Exception.

EOF
	file "musl (COPYRIGHT)" "$here/licenses/musl-COPYRIGHT"

	section "VM runtime: go-microvm-runner (go-microvm $rev)"
	cat <<EOF
go-microvm-runner is built from go-microvm
($(git -C "$src" remote get-url origin)), licensed under the Apache
License 2.0. The Go modules built into it and their licenses follow.

EOF
	file "go-microvm (LICENSE)" "$src/LICENSE"
	CGO_ENABLED=1 "$here/../go-notices.sh" "$src" "darwin/arm64 linux/amd64 linux/arm64" ./runner/cmd/go-microvm-runner
} >"$out/vm-runtime-notices.txt"

wc -c "$out/vm-runtime-notices.txt"
