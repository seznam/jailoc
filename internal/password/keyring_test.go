package password

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

type mockKeyring struct {
	getVal    string
	getErr    error
	setErr    error
	getCalled bool
	setCalled bool
}

func (m *mockKeyring) Get(_ string, _ string) (string, error) {
	m.getCalled = true
	return m.getVal, m.getErr
}

func (m *mockKeyring) Set(_ string, _ string, _ string) error {
	m.setCalled = true
	return m.setErr
}

type blockingKeyring struct {
	block chan struct{}
	val   string
}

func (b *blockingKeyring) Get(_ string, _ string) (string, error) {
	<-b.block
	return b.val, nil
}

func (b *blockingKeyring) Set(_ string, _ string, _ string) error {
	<-b.block
	return nil
}

func TestKeyringGet(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")
	tests := []struct {
		name         string
		getVal       string
		getErr       error
		wantVal      string
		wantErr      error
		wantContains string
	}{
		{name: "success", getVal: "secret", wantVal: "secret"},
		{name: "not_found", getErr: keyring.ErrNotFound, wantErr: keyring.ErrNotFound},
		{name: "other_error", getErr: errBoom, wantErr: errBoom, wantContains: "keyring get for workspace \"workspace-a\""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockKeyring{getVal: tc.getVal, getErr: tc.getErr}
			k := &timedKeyring{inner: mock, timeout: time.Second, interactive: false, w: io.Discard}

			got, err := k.Get(keyringService, "workspace-a")
			if !mock.getCalled {
				t.Fatal("expected inner keyring Get to be called")
			}

			if got != tc.wantVal {
				t.Fatalf("Get() value = %q, want %q", got, tc.wantVal)
			}

			if tc.wantErr == nil && err != nil {
				t.Fatalf("Get() unexpected error: %v", err)
			}
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("Get() expected error %v, got nil", tc.wantErr)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Get() error = %v, want error matching %v", err, tc.wantErr)
				}
				if tc.wantContains != "" && !strings.Contains(err.Error(), tc.wantContains) {
					t.Fatalf("Get() error %q does not contain %q", err.Error(), tc.wantContains)
				}
			}
		})
	}
}

func TestKeyringSet(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")
	tests := []struct {
		name         string
		setErr       error
		wantErr      error
		wantContains string
	}{
		{name: "success"},
		{name: "failure", setErr: errBoom, wantErr: errBoom, wantContains: "keyring set for workspace \"workspace-a\""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockKeyring{setErr: tc.setErr}
			k := &timedKeyring{inner: mock, timeout: time.Second, interactive: false, w: io.Discard}

			err := k.Set(keyringService, "workspace-a", "secret")
			if !mock.setCalled {
				t.Fatal("expected inner keyring Set to be called")
			}

			if tc.wantErr == nil && err != nil {
				t.Fatalf("Set() unexpected error: %v", err)
			}
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("Set() expected error %v, got nil", tc.wantErr)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Set() error = %v, want error matching %v", err, tc.wantErr)
				}
				if tc.wantContains != "" && !strings.Contains(err.Error(), tc.wantContains) {
					t.Fatalf("Set() error %q does not contain %q", err.Error(), tc.wantContains)
				}
			}
		})
	}
}

func TestKeyringTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "get_blocks_noninteractive",
			run: func(t *testing.T) {
				b := &blockingKeyring{block: make(chan struct{})}
				k := &timedKeyring{inner: b, timeout: 100 * time.Millisecond, interactive: false, w: io.Discard}

				start := time.Now()
				_, err := k.Get(keyringService, "workspace-a")
				dur := time.Since(start)

				if !errors.Is(err, ErrKeyringTimeout) {
					t.Fatalf("Get() error = %v, want %v", err, ErrKeyringTimeout)
				}
				if dur > 2*time.Second {
					t.Fatalf("Get() timeout took too long: %v", dur)
				}
			},
		},
		{
			name: "set_blocks_noninteractive",
			run: func(t *testing.T) {
				b := &blockingKeyring{block: make(chan struct{})}
				k := &timedKeyring{inner: b, timeout: 100 * time.Millisecond, interactive: false, w: io.Discard}

				start := time.Now()
				err := k.Set(keyringService, "workspace-a", "secret")
				dur := time.Since(start)

				if !errors.Is(err, ErrKeyringTimeout) {
					t.Fatalf("Set() error = %v, want %v", err, ErrKeyringTimeout)
				}
				if dur > 2*time.Second {
					t.Fatalf("Set() timeout took too long: %v", dur)
				}
			},
		},
		{
			name: "interactive_no_timeout",
			run: func(t *testing.T) {
				b := &blockingKeyring{block: make(chan struct{}), val: "secret"}
				k := &timedKeyring{inner: b, timeout: 100 * time.Millisecond, interactive: true, w: io.Discard}

				go func() {
					time.Sleep(50 * time.Millisecond)
					close(b.block)
				}()

				got, err := k.Get(keyringService, "workspace-a")
				if err != nil {
					t.Fatalf("Get() unexpected error: %v", err)
				}
				if got != "secret" {
					t.Fatalf("Get() = %q, want %q", got, "secret")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t)
		})
	}
}

func TestKeyringMessage(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	mock := &mockKeyring{getVal: "secret"}
	k := &timedKeyring{inner: mock, timeout: time.Second, interactive: true, w: &buf}

	if _, err := k.Get(keyringService, "workspace-a"); err != nil {
		t.Fatalf("Get() unexpected error: %v", err)
	}
	wantOnce := "Accessing system keyring...\n"
	if got := buf.String(); got != wantOnce {
		t.Fatalf("Get() message = %q, want %q", got, wantOnce)
	}

	if err := k.Set(keyringService, "workspace-a", "secret"); err != nil {
		t.Fatalf("Set() unexpected error: %v", err)
	}
	// Message should still appear only once — sync.Once deduplicates across Get/Set.
	if got := buf.String(); got != wantOnce {
		t.Fatalf("after Get+Set message = %q, want single %q", got, wantOnce)
	}
}
