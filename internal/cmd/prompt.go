package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/agent"
)

// promptIO configures input, output and mode for interactive huh forms and
// --yes non-interactive runs.
type promptIO struct {
	in         io.Reader
	out        io.Writer
	yes        bool
	accessible bool
	runHook    func(*huh.Form) error
}

func newStdinPromptIO(yes bool) *promptIO {
	return &promptIO{
		in:         os.Stdin,
		out:        os.Stdout,
		yes:        yes,
		accessible: isAccessibleEnv(),
	}
}

func isAccessibleEnv() bool {
	if os.Getenv("TERM") == "dumb" {
		return true
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv("ACCESSIBLE")))
	return v != "" && v != "0" && v != "false"
}

func (p *promptIO) reader() io.Reader {
	if p == nil || p.in == nil {
		return os.Stdin
	}
	return p.in
}

func (p *promptIO) writer() io.Writer {
	if p == nil || p.out == nil {
		return os.Stdout
	}
	return p.out
}

// compactTheme returns a compact, inline-friendly huh theme that respects
// --no-color / NO_COLOR.
func compactTheme() *huh.Theme {
	var t *huh.Theme
	if color.NoColor {
		t = huh.ThemeBase()
	} else {
		t = huh.ThemeBase16()
	}
	t.FieldSeparator = lipgloss.NewStyle().SetString("\n")
	t.Focused.NoteTitle = t.Focused.NoteTitle.MarginBottom(0)
	t.Blurred.NoteTitle = t.Blurred.NoteTitle.MarginBottom(0)
	return t
}

// runForm runs a huh form inline (never alt-screen) with compact styling,
// mapping user abort to context.Canceled.
func (p *promptIO) runForm(ctx context.Context, form *huh.Form) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	in := p.reader()
	out := p.writer()
	accessible := p != nil && p.accessible || isAccessibleEnv()

	form.WithTheme(compactTheme()).
		WithInput(in).
		WithOutput(out).
		WithAccessible(accessible)

	if !accessible {
		form.WithProgramOptions(
			tea.WithInput(in),
			tea.WithOutput(out),
			tea.WithContext(ctx),
		)
	}

	if p != nil && p.runHook != nil {
		return p.runHook(form)
	}
	err := form.RunWithContext(ctx)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, huh.ErrUserAborted) {
		return context.Canceled
	}
	return fmt.Errorf("prompt: %w", err)
}

// confirm runs a single inline yes/no prompt, or returns def immediately when
// p.yes is set.
func (p *promptIO) confirm(ctx context.Context, title, description string, def bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if p != nil && p.yes {
		return def, nil
	}
	ans := def
	field := huh.NewConfirm().
		Title(title).
		Value(&ans).
		Inline(description == "")
	if description != "" {
		field.Description(description)
	}
	form := huh.NewForm(huh.NewGroup(field))
	if err := p.runForm(ctx, form); err != nil {
		return false, err
	}
	return ans, nil
}

// providerMultiSelect builds a MultiSelect field for choosing model providers
// of a multi-provider coding agent.
func providerMultiSelect(title string, h agent.Harness, value *[]string) *huh.MultiSelect[string] {
	names := h.ProviderNames()
	opts := make([]huh.Option[string], len(names))
	for i, name := range names {
		selected := len(*value) == 0 || slices.Contains(*value, name)
		opts[i] = huh.NewOption(name, name).Selected(selected)
	}
	if len(*value) == 0 {
		*value = slices.Clone(names)
	}
	return huh.NewMultiSelect[string]().
		Title(cmp.Or(title, "Model providers")).
		Description("The agent reaches only the model and auth endpoints of the providers you select (models.dev is always included).").
		Options(opts...).
		Filterable(false).
		Value(value)
}

// normalizeProviders converts the selected provider slice into the slice
// expected by config rendering and migration: nil when every provider of h is
// selected, or the selected slice in h.ProviderNames() order (including empty
// slice []string{} when none are selected).
func normalizeProviders(h agent.Harness, selected []string) []string {
	if selected == nil {
		return nil
	}
	all := h.ProviderNames()
	out := make([]string, 0, len(selected))
	for _, name := range all {
		if slices.Contains(selected, name) {
			out = append(out, name)
		}
	}
	if len(out) == len(all) {
		return nil
	}
	return out
}
