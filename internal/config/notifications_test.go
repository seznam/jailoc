package config

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestValidateNotifications(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		defaults  string
		workspace string
		wantErr   string
	}{
		{name: "unset"},
		{name: "defaults auto", defaults: NotificationsAuto},
		{name: "defaults native", defaults: NotificationsNative},
		{name: "defaults osc", defaults: NotificationsOSC},
		{name: "workspace off", workspace: NotificationsOff},
		{name: "defaults unknown", defaults: "bell", wantErr: `defaults: invalid notifications "bell"`},
		{name: "workspace unknown", workspace: "Native", wantErr: `workspace "default": invalid notifications "Native"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{
				Defaults:   Defaults{Notifications: tt.defaults},
				Workspaces: map[string]Workspace{"default": {Notifications: tt.workspace}},
			}
			err := Validate(cfg)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDefaultConfigContentDocumentsNotifications(t *testing.T) {
	t.Parallel()

	var cfg Config
	if _, err := toml.Decode(strings.ReplaceAll(defaultConfigContent, "# notifications", "notifications"), &cfg); err != nil {
		t.Fatalf("decode default config with notifications uncommented: %v", err)
	}
	if cfg.Defaults.Notifications != NotificationsAuto || cfg.Workspaces["default"].Notifications != NotificationsAuto {
		t.Fatalf("uncommented notifications = %q (defaults), %q (workspace), want %q", cfg.Defaults.Notifications, cfg.Workspaces["default"].Notifications, NotificationsAuto)
	}
	if err := Validate(&cfg); err != nil {
		t.Fatalf("Validate(uncommented default config) = %v", err)
	}
}
