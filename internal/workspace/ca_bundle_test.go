package workspace_test

import (
	"testing"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestResolveCABundleAppliesInheritance(t *testing.T) {
	t.Parallel()

	automatic := config.AutomaticCABundle()
	disabled := config.DisabledCABundle()
	defaultCustom := config.CustomCABundle("/default.pem")
	workspaceCustom := config.CustomCABundle("/workspace.pem")

	tests := []struct {
		name        string
		defaultCA   *config.CABundle
		workspaceCA *config.CABundle
		want        config.CABundle
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
				Defaults: config.Defaults{CABundle: tt.defaultCA},
				Workspaces: map[string]config.Workspace{
					"test": {Paths: []string{"/data"}, CABundle: tt.workspaceCA},
				},
			}

			resolved, err := workspace.Resolve(cfg, "test")
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if resolved.CABundle != tt.want {
				t.Fatalf("CABundle = %#v, want %#v", resolved.CABundle, tt.want)
			}
		})
	}
}
