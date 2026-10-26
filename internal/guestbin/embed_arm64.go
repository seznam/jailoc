//go:build (darwin || linux) && arm64

package guestbin

import "embed"

//go:embed all:bin/arm64
var files embed.FS

func embedded() []byte {
	data, err := files.ReadFile("bin/arm64/" + FileName)
	if err != nil {
		return nil
	}
	return data
}
