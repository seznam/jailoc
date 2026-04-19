package password

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func safeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestReadPasswordFile(t *testing.T) {
	home := safeHome(t)

	tests := []struct {
		name         string
		content      string
		createFile   bool
		want         string
		wantErrIs    error
		wantContains string
	}{
		{
			name:       "valid_content",
			content:    "abc123\n",
			createFile: true,
			want:       "abc123",
		},
		{
			name:         "missing_file",
			wantErrIs:    os.ErrNotExist,
			wantContains: "read password file for workspace",
		},
		{
			name:         "empty_file",
			content:      "",
			createFile:   true,
			wantContains: "read password file for workspace",
		},
		{
			name:       "trailing_newline",
			content:    "mypassword\n",
			createFile: true,
			want:       "mypassword",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := "ws-" + tt.name
			path := filepath.Join(home, ".local", "share", "jailoc", workspace, "password")

			if tt.createFile {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
				}
				if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
					t.Fatalf("WriteFile(%q): %v", path, err)
				}
			}

			got, err := ReadPasswordFile(workspace)

			if tt.wantErrIs != nil || tt.wantContains != "" {
				if err == nil {
					t.Fatalf("ReadPasswordFile(%q) error = nil", workspace)
				}
				if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("ReadPasswordFile(%q) error = %v, want errors.Is(..., %v)", workspace, err, tt.wantErrIs)
				}
				if tt.wantContains != "" && !strings.Contains(err.Error(), tt.wantContains) {
					t.Fatalf("ReadPasswordFile(%q) error = %q, want substring %q", workspace, err.Error(), tt.wantContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("ReadPasswordFile(%q) error: %v", workspace, err)
			}
			if got != tt.want {
				t.Fatalf("ReadPasswordFile(%q) = %q, want %q", workspace, got, tt.want)
			}
		})
	}
}

func TestWritePasswordFile(t *testing.T) {
	home := safeHome(t)

	tests := []struct {
		name string
		run  func(t *testing.T, workspace string)
	}{
		{
			name: "creates_file",
			run: func(t *testing.T, workspace string) {
				if err := WritePasswordFile(workspace, "password"); err != nil {
					t.Fatalf("WritePasswordFile(%q) error: %v", workspace, err)
				}

				path := PasswordFilePath(workspace)
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("Stat(%q): %v", path, err)
				}
				if perm := info.Mode().Perm(); perm != 0o600 {
					t.Fatalf("file mode = %#o, want %#o", perm, 0o600)
				}
			},
		},
		{
			name: "creates_parent_dir",
			run: func(t *testing.T, workspace string) {
				if err := WritePasswordFile(workspace, "password"); err != nil {
					t.Fatalf("WritePasswordFile(%q) error: %v", workspace, err)
				}

				dirInfo, err := os.Stat(DataDir(workspace))
				if err != nil {
					t.Fatalf("Stat(%q): %v", DataDir(workspace), err)
				}
				if perm := dirInfo.Mode().Perm(); perm != 0o700 {
					t.Fatalf("dir mode = %#o, want %#o", perm, 0o700)
				}

				fileInfo, err := os.Stat(PasswordFilePath(workspace))
				if err != nil {
					t.Fatalf("Stat(%q): %v", PasswordFilePath(workspace), err)
				}
				if perm := fileInfo.Mode().Perm(); perm != 0o600 {
					t.Fatalf("file mode = %#o, want %#o", perm, 0o600)
				}
			},
		},
		{
			name: "file_already_exists",
			run: func(t *testing.T, workspace string) {
				if err := WritePasswordFile(workspace, "first"); err != nil {
					t.Fatalf("first WritePasswordFile(%q) error: %v", workspace, err)
				}
				if err := WritePasswordFile(workspace, "second"); err != nil {
					t.Fatalf("second WritePasswordFile(%q) error: %v", workspace, err)
				}

				raw, err := os.ReadFile(PasswordFilePath(workspace))
				if err != nil {
					t.Fatalf("ReadFile(%q): %v", PasswordFilePath(workspace), err)
				}
				if got := string(raw); got != "first" {
					t.Fatalf("password content = %q, want %q", got, "first")
				}
			},
		},
		{
			name: "parent_already_exists",
			run: func(t *testing.T, workspace string) {
				if err := os.MkdirAll(filepath.Join(home, ".local", "share", "jailoc", workspace), 0o700); err != nil {
					t.Fatalf("MkdirAll parent dir: %v", err)
				}

				if err := WritePasswordFile(workspace, "password"); err != nil {
					t.Fatalf("WritePasswordFile(%q) error: %v", workspace, err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.run(t, "ws-"+tt.name)
		})
	}
}

func TestWritePasswordFileRace(t *testing.T) {
	home := t.TempDir()

	oldHome, hadHome := os.LookupEnv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	t.Cleanup(func() {
		if !hadHome {
			_ = os.Unsetenv("HOME")
			return
		}
		_ = os.Setenv("HOME", oldHome)
	})

	const workspace = "race-ws"

	start := make(chan struct{})
	errCh := make(chan error, 2)

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			errCh <- WritePasswordFile(workspace, "password")
		})
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("WritePasswordFile(%q) concurrent call error: %v", workspace, err)
		}
	}

	raw, err := os.ReadFile(PasswordFilePath(workspace))
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", PasswordFilePath(workspace), err)
	}
	if got := string(raw); got != "password" {
		t.Fatalf("password content = %q, want %q", got, "password")
	}
}
