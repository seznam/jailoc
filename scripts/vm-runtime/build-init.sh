#!/usr/bin/env bash
# Build libkrun's guest init (init.krun) against musl libc.
#
# libkrun embeds a static init binary that runs as PID 1 in the guest. Its own
# build compiles it against the build host's glibc, which would put LGPL code
# into libkrun. jailoc builds it from the same libkrun source in a pinned
# Alpine image instead, statically linked against musl (MIT), and hands it to
# the libkrun build through KRUN_INIT_BINARY_PATH. The compiler flags match
# libkrun's src/init_blob/build.rs.
#
# Usage: build-init.sh GO_MICROVM_CHECKOUT OUT_DIR
#   GO_MICROVM_CHECKOUT  go-microvm checkout at the revision pinned in go.mod
#   OUT_DIR              receives init.krun and init.env (libkrun, musl and
#                        GCC versions)
#
# Requires: Linux on x86_64 or aarch64, git, podman or docker. The init is
# built for the host architecture.
set -euo pipefail

src=${1:?go-microvm checkout}
out=${2:?output directory}
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) arch=amd64 ;;
  Linux/aarch64) arch=arm64 ;;
  *) echo "must run on Linux x86_64 or aarch64" >&2; exit 1 ;;
esac
alpine=docker.io/library/alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
libkrun=$(grep '^LIBKRUN_VERSION=' "$src/versions.env" | cut -d= -f2)
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
runtime=$(command -v podman >/dev/null 2>&1 && echo podman || echo docker)

git clone --quiet --depth 1 --branch "$libkrun" https://github.com/libkrun/libkrun "$work/libkrun"
"$runtime" run --rm \
  -v "$work/libkrun/src/init_blob/init:/src:ro" -v "$out:/out" \
  -e LIBKRUN_VERSION="$libkrun" -e ARCH="$arch" \
  "$alpine" \
  sh -euc '
    apk add --no-cache gcc musl-dev linux-headers binutils >/dev/null
    cc -O2 -static -Wall -o /out/init.krun /src/init.c /src/dhcp.c
    if readelf -l /out/init.krun | grep -q INTERP; then
      echo "init.krun is not static" >&2
      exit 1
    fi
    musl=$(ldd --version 2>&1 | sed -n "s/^Version //p")
    printf "LIBKRUN_VERSION=%s\nARCH=%s\nMUSL_VERSION=%s\nGCC_VERSION=%s\n" \
      "$LIBKRUN_VERSION" "$ARCH" "$musl" "$(cc -dumpversion)" > /out/init.env
  '
cat "$out/init.env"
