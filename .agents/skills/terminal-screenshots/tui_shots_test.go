package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/fatih/color"
	"github.com/muesli/termenv"

	"github.com/seznam/jailoc/internal/config"
)

var (
	shotTrailingSpace = regexp.MustCompile(`[ ]+((?:\x1b\[[0-9;]*m)*)$`)
	shotANSI          = regexp.MustCompile(`\x1b\[[0-9;]*m`)
)

// shotFrame trims huh's width padding and squeezes blank lines.
func shotFrame(view, home string) string {
	view = strings.ReplaceAll(view, home, "/Users/you")
	var out []string
	blank := false
	for _, l := range strings.Split(view, "\n") {
		l = shotTrailingSpace.ReplaceAllString(l, "$1")
		isBlank := shotANSI.ReplaceAllString(l, "") == ""
		if isBlank && blank {
			continue
		}
		blank = isBlank
		out = append(out, l)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func TestZZTUIShots(t *testing.T) {
	dir := os.Getenv("SHOTS_DIR")
	if dir == "" {
		t.Skip("SHOTS_DIR not set")
	}
	lipgloss.SetColorProfile(termenv.ANSI)
	lipgloss.SetHasDarkBackground(true)
	color.NoColor = false

	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, d := range []string{"shop", "data"} {
		if err := os.Mkdir(filepath.Join(home, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".npmrc"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// frames drives form with Enter and returns every rendered view.
	frames := func(form *huh.Form) []string {
		form.WithTheme(compactTheme())
		if cmd := form.Init(); cmd != nil {
			flushCmd(form, cmd)
		}
		_, cmd := form.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		flushCmd(form, cmd)
		var views []string
		for i := 0; i < 50 && form.State == huh.StateNormal; i++ {
			views = append(views, shotFrame(form.View(), home))
			_, cmd := form.Update(tea.KeyMsg{Type: tea.KeyEnter})
			flushCmd(form, cmd)
		}
		if form.State != huh.StateCompleted {
			t.Fatalf("form did not complete: %v", form.State)
		}
		return views
	}
	write := func(name, cmd, header, frame string) {
		// The trailing hide-cursor sequence tells render.mjs not to draw a cursor.
		s := "$ " + cmd + "\n" + header + frame + "\n\x1b[?25l"
		if err := os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	opts := setupOptions{configPath: "~/.config/jailoc/config.toml", targetDir: "~/shop", defaultName: "shop"}
	ans := defaultFirstTimeAnswers(opts)
	ans.agent = "opencode"
	ans.wsName = "shop"
	ans.wsDir = "~/shop"
	views := frames(buildFirstTimeForm(&ans, opts))
	welcome := color.New(color.FgCyan, color.Bold).Sprintf("Welcome to jailoc! Let's configure %s.", opts.configPath) + "\n\n"
	write("setup-agent", "jailoc setup", welcome, views[0])
	write("setup-review", "jailoc setup", welcome, views[len(views)-1])

	cfg := &config.Config{
		Mode:     "remote",
		Defaults: config.Defaults{SSHAuthSock: true},
		Workspaces: map[string]config.Workspace{
			"shop": {Backend: config.BackendDocker, Paths: []string{home + "/shop"}, ExposePort: new(true),
				Mounts: []string{home + "/.npmrc:/home/agent/.npmrc:ro", home + "/data:/data:rw"}},
		},
	}
	mans, err := defaultMigrationAnswers(cfg, os.Stat)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range frames(buildMigrationForm(cfg, &mans, os.Stat)) {
		if strings.Contains(v, "This path is a file") {
			write("migrate-mounts", "jailoc migrate", "", v)
			return
		}
	}
	t.Fatal("no mount frame")
}
