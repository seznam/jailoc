//go:build (darwin || linux) && amd64

package guestbin

import "embed"

//go:embed all:bin/amd64
var files embed.FS

func embedded() []byte {
	data, err := files.ReadFile("bin/amd64/" + FileName)
	if err != nil {
		return nil
	}
	return data
}
