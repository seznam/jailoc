package vm

import "embed"

// runtimeFiles holds the runtime tarballs that release builds place in
// runtimebin/linux-arm64 before building jailoc.
//
//go:embed all:runtimebin/linux-arm64
var runtimeFiles embed.FS

const runtimeDir = "runtimebin/linux-arm64"
