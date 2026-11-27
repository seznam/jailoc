//go:build linux && amd64

package brokerbin

import "embed"

//go:embed all:bin/linux-amd64
var files embed.FS

func embedded() []byte {
	data, err := files.ReadFile("bin/linux-amd64/" + FileName)
	if err != nil {
		return nil
	}
	return data
}
