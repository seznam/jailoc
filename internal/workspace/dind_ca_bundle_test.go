package workspace_test

import (
	"testing"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestResolveDindCABundleAppliesInheritance(t *testing.T) {
	t.Parallel()

	automatic := config.AutomaticDindCABundle()
	disabled := config.DisabledDindCABundle()
	defaultCustom := config.CustomDindCABundle("/default.pem")
	workspaceCustom := config.CustomDindCABundle("/workspace.pem")

	tests := []struct {
		name        string
		defaultCA   *config.DindCABundle
		workspaceCA *config.DindCABundle
		want        config.DindCABundle
	}{
		{name: "both omitted defaults automatic", want: automatic},
		{name: "workspace inherits automatic", defaultCA: &automatic, want: automatic},
		{name: "workspace inherits disabled", defaultCA: &disabled, want: disabled},
		{name: "workspace inherits custom", defaultCA: &defaultCustom, want: defaultCustom},
		{name: "workspace false overrides automatic", defaultCA: &automatic, workspaceCA: &disabled, want: disabled},
		{name: "workspace true overrides disabled", defaultCA: &disabled, workspaceCA: &automatic, want: automatic},
		{name: "workspace custom overrides default", defaultCA: &defaultCustom, workspaceCA: &workspaceCustom, want: workspaceCustom},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{
				Defaults: config.Defaults{DindCABundle: tt.defaultCA},
				Workspaces: map[string]config.Workspace{
					"test": {Paths: []string{"/data"}, DindCABundle: tt.workspaceCA},
				},
			}

			resolved, err := workspace.Resolve(cfg, "test")
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if resolved.DindCABundle != tt.want {
				t.Fatalf("DindCABundle = %#v, want %#v", resolved.DindCABundle, tt.want)
			}
		})
	}
}
