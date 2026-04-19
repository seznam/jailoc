package password

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

func ReadPasswordFile(workspace string) (string, error) {
	raw, err := os.ReadFile(PasswordFilePath(workspace))
	if err != nil {
		return "", fmt.Errorf("read password file for workspace %q: %w", workspace, err)
	}

	password := strings.TrimSpace(string(raw))
	if password == "" {
		return "", fmt.Errorf("read password file for workspace %q: password file is empty", workspace)
	}

	return password, nil
}

func WritePasswordFile(workspace string, password string) error {
	if err := os.MkdirAll(DataDir(workspace), 0o700); err != nil {
		return fmt.Errorf("create password directory for workspace %q: %w", workspace, err)
	}

	path := PasswordFilePath(workspace)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: path is constructed from validated workspace name
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}

		return fmt.Errorf("open password file for workspace %q: %w", workspace, err)
	}

	if _, err := f.Write([]byte(password)); err != nil {
		_ = f.Close()
		return fmt.Errorf("write password file for workspace %q: %w", workspace, err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("close password file for workspace %q: %w", workspace, err)
	}

	return nil
}
