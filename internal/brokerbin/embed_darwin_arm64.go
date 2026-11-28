//go:build darwin && arm64

package brokerbin

import "embed"

//go:embed all:bin/darwin-arm64
var files embed.FS

func embedded() []byte {
	data, err := files.ReadFile("bin/darwin-arm64/" + FileName)
	if err != nil {
		return nil
	}
	return data
}
