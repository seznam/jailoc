#!/usr/bin/env bash
# Write THIRD_PARTY_NOTICES for jailoc release archives: the licenses of the Go
# modules built into jailoc (including the embedded jailoc-guest) followed by
# the notices of the embedded VM runtime (vm-runtime-notices.txt from
# scripts/vm-runtime/build-runtime-notices.sh).
#
# Usage: third-party-notices.sh VM_RUNTIME_NOTICES OUT_FILE
set -euo pipefail

notices=${1:?vm-runtime-notices.txt}
out=${2:?output file}
here=$(cd "$(dirname "$0")" && pwd)
test -s "$notices" || {
	echo "missing VM runtime notices $notices" >&2
	exit 1
}
mkdir -p "$(dirname "$out")"

{
	cat <<'EOF'
Third-party software in jailoc

jailoc is licensed under the MIT License (LICENSE). Release builds of jailoc
contain the third-party software listed below, each under its own license.

The Go modules are used unmodified. The source of each is available from the
Go module proxy (https://proxy.golang.org), for example with
"go mod download <module path>@<version>" for the module path and version
listed with it.

################################################################################
jailoc: Go modules
################################################################################

EOF
	CGO_ENABLED=0 "$here/go-notices.sh" "$here/.." "darwin/arm64 linux/amd64 linux/arm64" \
		./cmd/jailoc ./cmd/jailoc-guest
	cat "$notices"
} >"$out"
