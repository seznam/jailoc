package password

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

type osKeyring struct{}

func (k osKeyring) Get(service, user string) (string, error) {
	password, err := keyring.Get(service, user)
	if err != nil {
		return "", err
	}

	return password, nil
}

func (k osKeyring) Set(service, user, password string) error {
	if err := keyring.Set(service, user, password); err != nil {
		return err
	}

	return nil
}

type timedKeyring struct {
	inner       Keyring
	timeout     time.Duration
	interactive bool
	w           io.Writer
	msgOnce     sync.Once
}

func (k *timedKeyring) announceAccess() {
	if k.interactive {
		k.msgOnce.Do(func() {
			_, _ = io.WriteString(k.w, "Accessing system keyring...\n")
		})
	}
}

func (k *timedKeyring) Get(service, user string) (string, error) {
	if k.interactive {
		k.announceAccess()

		password, err := k.inner.Get(service, user)
		if err != nil {
			return "", fmt.Errorf("keyring get for workspace %q: %w", user, err)
		}

		return password, nil
	}

	type getResult struct {
		password string
		err      error
	}

	resultCh := make(chan getResult, 1)
	go func() {
		password, err := k.inner.Get(service, user)
		resultCh <- getResult{password: password, err: err}
	}()

	select {
	case result := <-resultCh:
		if result.err != nil {
			return "", fmt.Errorf("keyring get for workspace %q: %w", user, result.err)
		}

		return result.password, nil
	case <-time.After(k.timeout):
		return "", ErrKeyringTimeout
	}
}

func (k *timedKeyring) Set(service, user, password string) error {
	if k.interactive {
		k.announceAccess()

		if err := k.inner.Set(service, user, password); err != nil {
			return fmt.Errorf("keyring set for workspace %q: %w", user, err)
		}

		return nil
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- k.inner.Set(service, user, password)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("keyring set for workspace %q: %w", user, err)
		}

		return nil
	case <-time.After(k.timeout):
		return ErrKeyringTimeout
	}
}

func NewKeyring(interactive bool) Keyring {
	return &timedKeyring{
		inner:       osKeyring{},
		timeout:     time.Second,
		interactive: interactive,
		w:           os.Stderr,
	}
}
