package guest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// addHostsEntry maps hostname to 127.0.1.1 in the hosts file at path unless
// a line already names it, so tools such as sudo resolve the VM hostname.
func addHostsEntry(path, hostname string) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: fixed system path or test file
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		for _, name := range fields[min(1, len(fields)):] {
			if name == hostname {
				return nil
			}
		}
	}
	text := string(data)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += "127.0.1.1\t" + hostname + "\n"
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil { //nolint:gosec // G306: /etc/hosts is world-readable
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
