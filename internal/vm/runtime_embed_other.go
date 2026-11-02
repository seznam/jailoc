//go:build !((darwin && arm64) || (linux && (amd64 || arm64)))

package vm

import "embed"

// runtimeFiles is empty: jailoc has no VM runtime for this platform.
var runtimeFiles embed.FS

const runtimeDir = ""
