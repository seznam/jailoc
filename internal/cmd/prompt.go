package cmd

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"

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
	// lines feeds accessible prompts; created on first accessible prompt.
	lines *lineReader
}

// formStep is one page of a prompt form. huh's accessible mode ignores group
// hide functions and dynamic titles, so runAccessible evaluates hidden after
// the earlier answers and calls refresh to update text that depends on them.
type formStep struct {
	fields  []huh.Field
	hidden  func() bool
	refresh func()
}

type formSteps []formStep

// form builds the huh form for the interactive terminal UI.
func (s formSteps) form() *huh.Form {
	groups := make([]*huh.Group, len(s))
	for i, st := range s {
		g := huh.NewGroup(st.fields...)
		if st.hidden != nil {
			g = g.WithHideFunc(st.hidden)
		}
		groups[i] = g
	}
	return huh.NewForm(groups...)
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

// runForm runs the form steps inline (never alt-screen) with compact styling,
// or as plain line prompts in accessible mode. User abort maps to
// context.Canceled.
func (p *promptIO) runForm(ctx context.Context, steps formSteps) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(steps) == 0 {
		return nil
	}
	if p != nil && p.accessible {
		return p.runAccessible(ctx, steps)
	}

	in := p.reader()
	out := p.writer()
	form := steps.form().
		WithTheme(compactTheme()).
		WithInput(in).
		WithOutput(out).
		WithProgramOptions(
			tea.WithInput(in),
			tea.WithOutput(out),
			tea.WithContext(ctx),
		)

	if p != nil && p.runHook != nil {
		return p.runHook(form)
	}
	err := form.RunWithContext(ctx)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, huh.ErrUserAborted) {
		return context.Canceled
	}
	return fmt.Errorf("prompt: %w", err)
}

// runAccessible asks the visible steps' fields one line prompt at a time. It
// replaces huh's accessible runner, which asks hidden groups, ignores ctx and
// accepts every default once input ends.
func (p *promptIO) runAccessible(ctx context.Context, steps formSteps) error {
	if p.lines == nil {
		p.lines = &lineReader{src: p.reader()}
	}
	in := p.lines
	out := p.writer()
	theme := compactTheme()
	for _, st := range steps {
		if st.hidden != nil && st.hidden() {
			continue
		}
		if st.refresh != nil {
			st.refresh()
		}
		for _, f := range st.fields {
			f.WithTheme(theme)
			_ = f.Init()  // the returned command only drives the terminal UI
			_ = f.Focus() // same
			in.ctx = ctx
			// huh validates an input's raw line before falling back to the
			// current value, so an empty line would fail validation instead
			// of keeping the default.
			in.emptyLine = ""
			if input, ok := f.(*huh.Input); ok {
				if v, ok := input.GetValue().(string); ok {
					in.emptyLine = v
				}
			}
			if err := f.RunAccessible(out, in); err != nil {
				return fmt.Errorf("prompt: %w", err)
			}
			_, _ = fmt.Fprintln(out)
			if err := ctx.Err(); err != nil {
				return err
			}
			if in.err != nil {
				if errors.Is(in.err, io.EOF) {
					return errors.New("prompt: input ended before all questions were answered")
				}
				return fmt.Errorf("prompt: read input: %w", in.err)
			}
		}
	}
	return nil
}

// lineReader hands accessible prompts one input line per Read. huh creates a
// new bufio.Scanner for every prompt, so returning more than a line would let
// one prompt swallow the answers to the next ones. It records the first read
// error (EOF included) so a prompt that fell back to its default can be
// rejected, and stops waiting for input when ctx ends.
type lineReader struct {
	src   io.Reader
	ctx   context.Context
	once  sync.Once
	lines chan lineResult
	buf   []byte
	err   error

	// emptyLine replaces an empty input line when set.
	emptyLine string
}

type lineResult struct {
	line []byte
	err  error
}

func (r *lineReader) Read(b []byte) (int, error) {
	if len(r.buf) > 0 {
		n := copy(b, r.buf)
		r.buf = r.buf[n:]
		return n, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	r.once.Do(r.start)
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case res := <-r.lines:
		if res.err != nil {
			r.err = res.err
			return 0, res.err
		}
		line := res.line
		if r.emptyLine != "" && strings.TrimSpace(string(line)) == "" {
			line = []byte(r.emptyLine + "\n")
		}
		n := copy(b, line)
		r.buf = line[n:]
		return n, nil
	}
}

// start reads src in the background, so a blocked read never outlives ctx
// for the caller. The goroutine reads at most one line ahead.
func (r *lineReader) start() {
	r.lines = make(chan lineResult)
	go func() {
		br := bufio.NewReader(r.src)
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				if line[len(line)-1] != '\n' {
					line = append(line, '\n')
				}
				r.lines <- lineResult{line: line}
			}
			if err != nil {
				r.lines <- lineResult{err: err}
				return
			}
		}
	}()
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
	if err := p.runForm(ctx, formSteps{{fields: []huh.Field{field}}}); err != nil {
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
