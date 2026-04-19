package password

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
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

type Resolver struct {
	keyring Keyring
	mode    string
}

func NewResolver(kr Keyring, mode string) *Resolver {
	if mode == "" {
		mode = ModeAuto
	}

	return &Resolver{keyring: kr, mode: mode}
}

func DefaultResolver(interactive bool, mode string) *Resolver {
	return NewResolver(NewKeyring(interactive), mode)
}

func (r *Resolver) Resolve(workspace string) (password string, source string, err error) {
	password, source, err = r.resolve(workspace)
	if err != nil {
		return "", "", fmt.Errorf("resolve password for workspace %q: %w", workspace, err)
	}

	return password, source, nil
}

func (r *Resolver) resolve(workspace string) (string, string, error) {
	switch r.mode {
	case ModeAuto:
		if value := os.Getenv(envKey); value != "" {
			return value, SourceEnv, nil
		}

		if value, err := r.keyring.Get(keyringService, workspace); err == nil {
			return value, SourceKeyring, nil
		}

		if value, err := ReadPasswordFile(workspace); err == nil {
			return value, SourceFile, nil
		}

		generated, err := Generate()
		if err != nil {
			return "", "", err
		}

		_ = r.keyring.Set(keyringService, workspace, generated)
		if err := WritePasswordFile(workspace, generated); err != nil {
			return "", "", err
		}

		return generated, SourceFile, nil

	case ModeEnv:
		if value := os.Getenv(envKey); value != "" {
			return value, SourceEnv, nil
		}

		return "", "", errors.New("OPENCODE_SERVER_PASSWORD not set (password_mode=env)")

	case ModeKeyring:
		value, err := r.keyring.Get(keyringService, workspace)
		if err == nil {
			return value, SourceKeyring, nil
		}

		if !errors.Is(err, keyring.ErrNotFound) {
			return "", "", err
		}

		generated, err := Generate()
		if err != nil {
			return "", "", err
		}

		if err := r.keyring.Set(keyringService, workspace, generated); err != nil {
			return "", "", err
		}

		return generated, SourceKeyring, nil

	case ModeFile:
		if value, err := ReadPasswordFile(workspace); err == nil {
			return value, SourceFile, nil
		}

		generated, err := Generate()
		if err != nil {
			return "", "", err
		}

		if err := WritePasswordFile(workspace, generated); err != nil {
			return "", "", err
		}

		return generated, SourceFile, nil

	default:
		return "", "", fmt.Errorf("unsupported password mode %q", r.mode)
	}
}

func DataDir(workspace string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("~", ".local", "share", "jailoc", workspace)
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
