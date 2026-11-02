package vm

import "embed"

// runtimeFiles holds the runtime tarballs that release builds place in
// runtimebin/darwin-arm64 before building jailoc.
//
//go:embed all:runtimebin/darwin-arm64
var runtimeFiles embed.FS

const runtimeDir = "runtimebin/darwin-arm64"
