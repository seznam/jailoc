package password

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestGenerate(t *testing.T) {
	t.Parallel()

	first, err := Generate()
	if err != nil {
		t.Fatalf("Generate() first call error: %v", err)
	}
	second, err := Generate()
	if err != nil {
		t.Fatalf("Generate() second call error: %v", err)
	}

	re := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if !re.MatchString(first) {
		t.Fatalf("first password has invalid format: %q", first)
	}
	if !re.MatchString(second) {
		t.Fatalf("second password has invalid format: %q", second)
	}
	if first == second {
		t.Fatal("expected two generated passwords to differ")
	}
}

func TestDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := DataDir("my-workspace")
	want := filepath.Join(home, ".local", "share", "jailoc", "my-workspace")

	if got != want {
		t.Fatalf("DataDir() = %q, want %q", got, want)
	}
}

func TestPasswordFilePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := PasswordFilePath("my-workspace")
	want := filepath.Join(home, ".local", "share", "jailoc", "my-workspace", "password")

	if got != want {
		t.Fatalf("PasswordFilePath() = %q, want %q", got, want)
	}
}

func TestResolve(t *testing.T) {
	errBoom := errors.New("boom")

	tests := []struct {
		name          string
		parallel      bool
		mode          string
		workspace     string
		envSet        bool
		envVal        string
		setupHome     bool
		setupFile     bool
		fileVal       string
		setupFileFail bool
		keyring       *mockKeyring
		wantSource    string
		wantValue     string
		wantErr       bool
		wantErrIs     error
		wantErrParts  []string
		assert        func(t *testing.T, tcName string, kr *mockKeyring, home string, workspace string, got string)
	}{
		{
			name:       "auto_env_var_set",
			mode:       ModeAuto,
			workspace:  "ws-auto-env-set",
			envSet:     true,
			envVal:     "mysecret",
			keyring:    &mockKeyring{},
			wantValue:  "mysecret",
			wantSource: SourceEnv,
			assert: func(t *testing.T, _ string, kr *mockKeyring, _ string, _ string, _ string) {
				t.Helper()
				if kr.getCalled || kr.setCalled {
					t.Fatalf("expected keyring not to be used when env is set; getCalled=%v setCalled=%v", kr.getCalled, kr.setCalled)
				}
			},
		},
		{
			name:       "auto_env_var_empty",
			mode:       ModeAuto,
			workspace:  "ws-auto-env-empty",
			envSet:     true,
			envVal:     "",
			keyring:    &mockKeyring{getVal: "from-keyring"},
			wantValue:  "from-keyring",
			wantSource: SourceKeyring,
			assert: func(t *testing.T, _ string, kr *mockKeyring, _ string, _ string, _ string) {
				t.Helper()
				if !kr.getCalled {
					t.Fatal("expected keyring Get to be called")
				}
			},
		},
		{
			name:       "auto_keyring_hit",
			mode:       ModeAuto,
			workspace:  "ws-auto-keyring-hit",
			keyring:    &mockKeyring{getVal: "from-keyring"},
			wantValue:  "from-keyring",
			wantSource: SourceKeyring,
			assert: func(t *testing.T, _ string, kr *mockKeyring, _ string, _ string, _ string) {
				t.Helper()
				if !kr.getCalled {
					t.Fatal("expected keyring Get to be called")
				}
				if kr.setCalled {
					t.Fatal("expected keyring Set not to be called")
				}
			},
		},
		{
			name:       "auto_keyring_miss_file_hit",
			mode:       ModeAuto,
			workspace:  "ws-auto-keyring-miss-file-hit",
			setupHome:  true,
			setupFile:  true,
			fileVal:    "from-file",
			keyring:    &mockKeyring{getErr: keyring.ErrNotFound},
			wantValue:  "from-file",
			wantSource: SourceFile,
			assert: func(t *testing.T, _ string, kr *mockKeyring, _ string, _ string, _ string) {
				t.Helper()
				if !kr.getCalled {
					t.Fatal("expected keyring Get to be called")
				}
				if kr.setCalled {
					t.Fatal("expected keyring Set not to be called")
				}
			},
		},
		{
			name:       "auto_keyring_timeout_file_hit",
			mode:       ModeAuto,
			workspace:  "ws-auto-keyring-timeout-file-hit",
			setupHome:  true,
			setupFile:  true,
			fileVal:    "from-file",
			keyring:    &mockKeyring{getErr: ErrKeyringTimeout},
			wantValue:  "from-file",
			wantSource: SourceFile,
			assert: func(t *testing.T, _ string, kr *mockKeyring, _ string, _ string, _ string) {
				t.Helper()
				if !kr.getCalled {
					t.Fatal("expected keyring Get to be called")
				}
			},
		},
		{
			name:       "auto_all_miss_generate",
			mode:       ModeAuto,
			workspace:  "ws-auto-all-miss-generate",
			setupHome:  true,
			keyring:    &mockKeyring{getErr: keyring.ErrNotFound},
			wantSource: SourceKeyring,
			assert: func(t *testing.T, tcName string, kr *mockKeyring, _ string, workspace string, got string) {
				t.Helper()
				if !kr.getCalled {
					t.Fatal("expected keyring Get to be called")
				}
				if !kr.setCalled {
					t.Fatal("expected keyring Set to be called for generated password")
				}
				re := regexp.MustCompile(`^[0-9a-f]{64}$`)
				if !re.MatchString(got) {
					t.Fatalf("%s: generated password has invalid format: %q", tcName, got)
				}

				stored, err := ReadPasswordFile(workspace)
				if err != nil {
					t.Fatalf("ReadPasswordFile(%q) error: %v", workspace, err)
				}
				if stored != "keyring" {
					t.Fatalf("password file = %q, want keyring marker", stored)
				}
			},
		},
		{
			name:       "auto_generate_keyring_store_fails",
			mode:       ModeAuto,
			workspace:  "ws-auto-generate-keyring-store-fails",
			setupHome:  true,
			keyring:    &mockKeyring{getErr: keyring.ErrNotFound, setErr: errBoom},
			wantSource: SourceFile,
			assert: func(t *testing.T, tcName string, kr *mockKeyring, _ string, workspace string, got string) {
				t.Helper()
				if !kr.setCalled {
					t.Fatal("expected keyring Set to be called")
				}
				re := regexp.MustCompile(`^[0-9a-f]{64}$`)
				if !re.MatchString(got) {
					t.Fatalf("%s: generated password has invalid format: %q", tcName, got)
				}
				stored, err := ReadPasswordFile(workspace)
				if err != nil {
					t.Fatalf("ReadPasswordFile(%q) error: %v", workspace, err)
				}
				if stored != got {
					t.Fatalf("stored password = %q, want %q", stored, got)
				}
			},
		},
		{
			name:          "auto_generate_file_store_fails",
			mode:          ModeAuto,
			workspace:     "ws-auto-generate-file-store-fails",
			setupHome:     true,
			setupFileFail: true,
			keyring:       &mockKeyring{getErr: keyring.ErrNotFound},
			wantErr:       true,
			wantErrParts:  []string{"resolve password for workspace", "create password directory for workspace"},
			assert: func(t *testing.T, _ string, kr *mockKeyring, _ string, _ string, _ string) {
				t.Helper()
				if !kr.setCalled {
					t.Fatal("expected keyring Set to be called before file write")
				}
			},
		},
		{
			name:       "auto_keyring_error_not_timeout",
			mode:       ModeAuto,
			workspace:  "ws-auto-keyring-error-not-timeout",
			setupHome:  true,
			setupFile:  true,
			fileVal:    "from-file",
			keyring:    &mockKeyring{getErr: errBoom},
			wantValue:  "from-file",
			wantSource: SourceFile,
		},
		{
			name:         "auto_keyring_fail_marker_file",
			mode:         ModeAuto,
			workspace:    "ws-auto-keyring-fail-marker",
			setupHome:    true,
			setupFile:    true,
			fileVal:      "keyring",
			keyring:      &mockKeyring{getErr: errBoom},
			wantErr:      true,
			wantErrParts: []string{"resolve password for workspace", "keyring is unavailable", "delete"},
		},
		{
			name:       "auto_keyring_deleted_marker_regenerate",
			mode:       ModeAuto,
			workspace:  "ws-auto-keyring-deleted-marker",
			setupHome:  true,
			setupFile:  true,
			fileVal:    "keyring",
			keyring:    &mockKeyring{getErr: keyring.ErrNotFound},
			wantSource: SourceKeyring,
			assert: func(t *testing.T, tcName string, kr *mockKeyring, _ string, _ string, got string) {
				t.Helper()
				if !kr.setCalled {
					t.Fatal("expected keyring Set to be called for regenerated password")
				}
				re := regexp.MustCompile(`^[0-9a-f]{64}$`)
				if !re.MatchString(got) {
					t.Fatalf("%s: regenerated password has invalid format: %q", tcName, got)
				}
			},
		},
		{
			name:         "auto_keyring_deleted_marker_restore_fails",
			mode:         ModeAuto,
			workspace:    "ws-auto-keyring-deleted-restore-fails",
			setupHome:    true,
			setupFile:    true,
			fileVal:      "keyring",
			keyring:      &mockKeyring{getErr: keyring.ErrNotFound, setErr: errBoom},
			wantErr:      true,
			wantErrParts: []string{"keyring entry deleted", "cannot restore", "delete"},
		},
		{
			name:       "env_mode_set",
			mode:       ModeEnv,
			workspace:  "ws-env-mode-set",
			envSet:     true,
			envVal:     "from-env-mode",
			keyring:    &mockKeyring{},
			wantValue:  "from-env-mode",
			wantSource: SourceEnv,
			assert: func(t *testing.T, _ string, kr *mockKeyring, _ string, _ string, _ string) {
				t.Helper()
				if kr.getCalled || kr.setCalled {
					t.Fatalf("expected keyring not to be used in env mode; getCalled=%v setCalled=%v", kr.getCalled, kr.setCalled)
				}
			},
		},
		{
			name:         "env_mode_empty",
			mode:         ModeEnv,
			workspace:    "ws-env-mode-empty",
			envSet:       true,
			envVal:       "",
			keyring:      &mockKeyring{},
			wantErr:      true,
			wantErrParts: []string{"resolve password for workspace", "OPENCODE_SERVER_PASSWORD not set", "password_mode=env"},
		},
		{
			name:         "env_mode_unset",
			mode:         ModeEnv,
			workspace:    "ws-env-mode-unset",
			keyring:      &mockKeyring{},
			wantErr:      true,
			wantErrParts: []string{"resolve password for workspace", "OPENCODE_SERVER_PASSWORD not set", "password_mode=env"},
		},
		{
			name:       "keyring_mode_hit",
			parallel:   true,
			mode:       ModeKeyring,
			workspace:  "ws-keyring-mode-hit",
			keyring:    &mockKeyring{getVal: "from-keyring"},
			wantValue:  "from-keyring",
			wantSource: SourceKeyring,
		},
		{
			name:       "keyring_mode_miss",
			parallel:   true,
			mode:       ModeKeyring,
			workspace:  "ws-keyring-mode-miss",
			keyring:    &mockKeyring{getErr: keyring.ErrNotFound},
			wantSource: SourceKeyring,
			assert: func(t *testing.T, tcName string, kr *mockKeyring, _ string, _ string, got string) {
				t.Helper()
				if !kr.setCalled {
					t.Fatal("expected keyring Set to be called")
				}
				re := regexp.MustCompile(`^[0-9a-f]{64}$`)
				if !re.MatchString(got) {
					t.Fatalf("%s: generated password has invalid format: %q", tcName, got)
				}
			},
		},
		{
			name:         "keyring_mode_error",
			parallel:     true,
			mode:         ModeKeyring,
			workspace:    "ws-keyring-mode-error",
			keyring:      &mockKeyring{getErr: errBoom},
			wantErr:      true,
			wantErrIs:    errBoom,
			wantErrParts: []string{"resolve password for workspace"},
		},
		{
			name:         "keyring_mode_timeout",
			parallel:     true,
			mode:         ModeKeyring,
			workspace:    "ws-keyring-mode-timeout",
			keyring:      &mockKeyring{getErr: ErrKeyringTimeout},
			wantErr:      true,
			wantErrIs:    ErrKeyringTimeout,
			wantErrParts: []string{"resolve password for workspace"},
		},
		{
			name:       "file_mode_hit",
			mode:       ModeFile,
			workspace:  "ws-file-mode-hit",
			setupHome:  true,
			setupFile:  true,
			fileVal:    "from-file",
			keyring:    &mockKeyring{},
			wantValue:  "from-file",
			wantSource: SourceFile,
			assert: func(t *testing.T, _ string, kr *mockKeyring, _ string, _ string, _ string) {
				t.Helper()
				if kr.getCalled || kr.setCalled {
					t.Fatalf("expected keyring not to be used in file mode; getCalled=%v setCalled=%v", kr.getCalled, kr.setCalled)
				}
			},
		},
		{
			name:       "file_mode_miss",
			mode:       ModeFile,
			workspace:  "ws-file-mode-miss",
			setupHome:  true,
			keyring:    &mockKeyring{},
			wantSource: SourceFile,
			assert: func(t *testing.T, tcName string, kr *mockKeyring, _ string, workspace string, got string) {
				t.Helper()
				if kr.getCalled || kr.setCalled {
					t.Fatalf("expected keyring not to be used in file mode; getCalled=%v setCalled=%v", kr.getCalled, kr.setCalled)
				}
				re := regexp.MustCompile(`^[0-9a-f]{64}$`)
				if !re.MatchString(got) {
					t.Fatalf("%s: generated password has invalid format: %q", tcName, got)
				}
				stored, err := ReadPasswordFile(workspace)
				if err != nil {
					t.Fatalf("ReadPasswordFile(%q) error: %v", workspace, err)
				}
				if stored != got {
					t.Fatalf("stored password = %q, want %q", stored, got)
				}
			},
		},
		{
			name:          "file_mode_write_fails",
			mode:          ModeFile,
			workspace:     "ws-file-mode-write-fails",
			setupHome:     true,
			setupFileFail: true,
			keyring:       &mockKeyring{},
			wantErr:       true,
			wantErrParts:  []string{"resolve password for workspace", "create password directory for workspace"},
		},
		{
			name:       "newresolver_empty_mode_defaults_to_auto",
			mode:       "",
			workspace:  "ws-newresolver-empty-mode-defaults-to-auto",
			keyring:    &mockKeyring{getVal: "from-keyring"},
			wantValue:  "from-keyring",
			wantSource: SourceKeyring,
		},
		{
			name:       "defaultresolver_mode_propagated",
			mode:       ModeEnv,
			workspace:  "ws-defaultresolver-mode-propagated",
			envSet:     true,
			envVal:     "from-default-resolver",
			wantValue:  "from-default-resolver",
			wantSource: SourceEnv,
			assert: func(t *testing.T, _ string, _ *mockKeyring, _ string, _ string, _ string) {
				t.Helper()
				r := DefaultResolver(false, ModeEnv)
				if r.mode != ModeEnv {
					t.Fatalf("DefaultResolver mode = %q, want %q", r.mode, ModeEnv)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.parallel {
				t.Parallel()
			}

			if tc.envSet {
				t.Setenv(envKey, tc.envVal)
			} else if tc.mode == ModeAuto || tc.mode == ModeEnv || tc.mode == "" {
				t.Setenv(envKey, "")
			}

			home := ""
			if tc.setupHome {
				home = safeHome(t)
			}

			if tc.setupFile {
				if err := WritePasswordFile(tc.workspace, tc.fileVal); err != nil {
					t.Fatalf("WritePasswordFile(%q) setup error: %v", tc.workspace, err)
				}
			}

			if tc.setupFileFail {
				if home == "" {
					t.Fatal("invalid test setup: setupFileFail requires setupHome")
				}
				if err := os.WriteFile(filepath.Join(home, ".local"), []byte("x"), 0o600); err != nil {
					t.Fatalf("WriteFile conflict .local path: %v", err)
				}
			}

			resolver := NewResolver(tc.keyring, tc.mode)
			got, source, err := resolver.Resolve(tc.workspace)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("Resolve(%q) error = nil, want error", tc.workspace)
				}
				if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("Resolve(%q) error = %v, want errors.Is(..., %v)", tc.workspace, err, tc.wantErrIs)
				}
				for _, part := range tc.wantErrParts {
					if !strings.Contains(err.Error(), part) {
						t.Fatalf("Resolve(%q) error = %q, want substring %q", tc.workspace, err.Error(), part)
					}
				}
				if strings.Contains(err.Error(), got) && got != "" {
					t.Fatalf("error unexpectedly includes returned password value")
				}
				return
			}

			if err != nil {
				t.Fatalf("Resolve(%q) unexpected error: %v", tc.workspace, err)
			}
			if tc.wantSource != source {
				t.Fatalf("Resolve(%q) source = %q, want %q", tc.workspace, source, tc.wantSource)
			}
			if tc.wantValue != "" && tc.wantValue != got {
				t.Fatalf("Resolve(%q) value = %q, want %q", tc.workspace, got, tc.wantValue)
			}
			if tc.assert != nil {
				tc.assert(t, tc.name, tc.keyring, home, tc.workspace, got)
			}
		})
	}

	t.Run("defaultresolver_empty_mode_defaults_to_auto", func(t *testing.T) {
		t.Parallel()
		r := DefaultResolver(false, "")
		if r.mode != ModeAuto {
			t.Fatalf("DefaultResolver empty mode = %q, want %q", r.mode, ModeAuto)
		}
	})

	t.Run("resolve_wraps_workspace_context", func(t *testing.T) {
		t.Setenv(envKey, "")
		workspace := "ws-wraps-context"
		_, _, err := NewResolver(&mockKeyring{}, ModeEnv).Resolve(workspace)
		if err == nil {
			t.Fatal("Resolve() expected error, got nil")
		}
		want := fmt.Sprintf("resolve password for workspace %q", workspace)
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Resolve() error = %q, want substring %q", err.Error(), want)
		}
	})
}

func TestPeek(t *testing.T) {
	tests := []struct {
		name       string
		parallel   bool
		mode       string
		workspace  string
		envSet     bool
		envVal     string
		setupHome  bool
		setupFile  bool
		fileVal    string
		keyring    *mockKeyring
		wantSource string
		wantErr    bool
	}{
		{
			name:       "auto_env_set",
			mode:       ModeAuto,
			workspace:  "ws-peek-auto-env",
			envSet:     true,
			envVal:     "secret",
			keyring:    &mockKeyring{},
			wantSource: SourceEnv,
		},
		{
			name:       "auto_file_exists",
			mode:       ModeAuto,
			workspace:  "ws-peek-auto-file",
			setupHome:  true,
			setupFile:  true,
			fileVal:    "from-file",
			keyring:    &mockKeyring{getErr: keyring.ErrNotFound},
			wantSource: SourceFile,
		},
		{
			name:       "auto_marker_file",
			mode:       ModeAuto,
			workspace:  "ws-peek-auto-marker",
			setupHome:  true,
			setupFile:  true,
			fileVal:    "keyring",
			keyring:    &mockKeyring{getErr: keyring.ErrNotFound},
			wantSource: SourceKeyring,
		},
		{
			name:       "auto_keyring_hit_no_file",
			mode:       ModeAuto,
			workspace:  "ws-peek-auto-keyring",
			setupHome:  true,
			keyring:    &mockKeyring{getVal: "from-keyring"},
			wantSource: SourceKeyring,
		},
		{
			name:       "auto_nothing",
			mode:       ModeAuto,
			workspace:  "ws-peek-auto-nothing",
			setupHome:  true,
			keyring:    &mockKeyring{getErr: keyring.ErrNotFound},
			wantSource: "",
		},
		{
			name:       "env_set",
			mode:       ModeEnv,
			workspace:  "ws-peek-env-set",
			envSet:     true,
			envVal:     "from-env",
			keyring:    &mockKeyring{},
			wantSource: SourceEnv,
		},
		{
			name:       "env_unset",
			mode:       ModeEnv,
			workspace:  "ws-peek-env-unset",
			keyring:    &mockKeyring{},
			wantSource: "",
		},
		{
			name:       "keyring_hit",
			parallel:   true,
			mode:       ModeKeyring,
			workspace:  "ws-peek-keyring-hit",
			keyring:    &mockKeyring{getVal: "from-keyring"},
			wantSource: SourceKeyring,
		},
		{
			name:       "keyring_miss",
			parallel:   true,
			mode:       ModeKeyring,
			workspace:  "ws-peek-keyring-miss",
			keyring:    &mockKeyring{getErr: keyring.ErrNotFound},
			wantSource: "",
		},
		{
			name:       "file_hit",
			mode:       ModeFile,
			workspace:  "ws-peek-file-hit",
			setupHome:  true,
			setupFile:  true,
			fileVal:    "from-file",
			keyring:    &mockKeyring{},
			wantSource: SourceFile,
		},
		{
			name:       "file_miss",
			mode:       ModeFile,
			workspace:  "ws-peek-file-miss",
			setupHome:  true,
			keyring:    &mockKeyring{},
			wantSource: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.parallel {
				t.Parallel()
			}

			if tc.envSet {
				t.Setenv(envKey, tc.envVal)
			} else if tc.mode == ModeAuto || tc.mode == ModeEnv {
				t.Setenv(envKey, "")
			}

			if tc.setupHome {
				safeHome(t)
			}

			if tc.setupFile {
				if err := WritePasswordFile(tc.workspace, tc.fileVal); err != nil {
					t.Fatalf("WritePasswordFile setup: %v", err)
				}
			}

			resolver := NewResolver(tc.keyring, tc.mode)
			source, err := resolver.Peek(tc.workspace)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("Peek(%q) error = nil, want error", tc.workspace)
				}
				return
			}
			if err != nil {
				t.Fatalf("Peek(%q) unexpected error: %v", tc.workspace, err)
			}
			if source != tc.wantSource {
				t.Fatalf("Peek(%q) source = %q, want %q", tc.workspace, source, tc.wantSource)
			}
		})
	}
}
