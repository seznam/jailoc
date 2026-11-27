//go:build linux && arm64

package brokerbin

import "embed"

//go:embed all:bin/linux-arm64
var files embed.FS

func embedded() []byte {
	data, err := files.ReadFile("bin/linux-arm64/" + FileName)
	if err != nil {
		return nil
	}
	return data
}
