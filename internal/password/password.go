package password

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	keyringService   = "jailoc"
	passwordLen      = 32
	passwordFileName = "password"
	envKey           = "OPENCODE_SERVER_PASSWORD"
)

const (
	SourceEnv     = "env"
	SourceKeyring = "keyring"
	SourceFile    = "file"
)

const (
	ModeAuto    = "auto"
	ModeEnv     = "env"
	ModeKeyring = "keyring"
	ModeFile    = "file"
)

var ErrKeyringTimeout = errors.New("keyring operation timed out")

type Keyring interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
}

func DataDir(workspace string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		panic(fmt.Errorf("data dir for workspace %q: %w", workspace, err))
	}

	return filepath.Join(home, ".local", "share", "jailoc", workspace)
}

func PasswordFilePath(workspace string) string {
	return filepath.Join(DataDir(workspace), passwordFileName)
}

func Generate() (string, error) {
	raw := make([]byte, passwordLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate random password bytes: %w", err)
	}

	return hex.EncodeToString(raw), nil
}
