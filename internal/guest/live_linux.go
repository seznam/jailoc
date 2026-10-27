package guest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// originalCA keeps the image's system CA bundle so a changed or removed
// workspace bundle can be applied on top of the original.
const originalCA = "/run/jailoc-system-ca.crt"

// liveState applies Reload requests: the CA bundle and the SSH agent relay.
type liveState struct {
	logger    *slog.Logger
	acct      account
	seq       int64
	applied   Reload
	stopAgent func()
}

func (l *liveState) apply(r Reload) error {
	if err := setCABundle(r.CABundle); err != nil {
		return err
	}
	switch {
	case r.SSHAgent && l.stopAgent == nil:
		stop, err := startAgentRelay(l.logger, l.acct)
		if err != nil {
			return err
		}
		l.stopAgent = stop
	case !r.SSHAgent && l.stopAgent != nil:
		l.stopAgent()
		l.stopAgent = nil
	}
	l.applied = r
	return nil
}

// poll applies a new reload request from the control share, if any.
func (l *liveState) poll() {
	data, err := os.ReadFile(filepath.Join(ControlDir, ReloadFile))
	if err != nil {
		return
	}
	var r Reload
	if err := json.Unmarshal(data, &r); err != nil || r.Seq == l.seq {
		return
	}
	l.seq = r.Seq
	done := ReloadDone{Seq: r.Seq}
	if err := l.apply(r); err != nil {
		l.logger.Error("reload", "error", err)
		done.Error = err.Error()
	} else {
		l.logger.Info("reload applied", "seq", r.Seq, "ca_bundle", r.CABundle != "", "ssh_agent", r.SSHAgent)
	}
	out, _ := json.Marshal(done)
	tmp := filepath.Join(ControlDir, ReloadDoneFile+".tmp")
	if err := os.WriteFile(tmp, out, 0o600); err == nil {
		_ = os.Rename(tmp, filepath.Join(ControlDir, ReloadDoneFile))
	}
}

// setCABundle makes the system CA bundle the image's original bundle plus
// the workspace bundle at path, or the original alone when path is empty or
// missing.
func setCABundle(path string) error {
	orig, err := os.ReadFile(originalCA)
	if errors.Is(err, fs.ErrNotExist) {
		orig, err = os.ReadFile(systemCA)
		if errors.Is(err, fs.ErrNotExist) {
			orig, err = nil, nil
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", systemCA, err)
		}
		if err := os.WriteFile(originalCA, orig, 0o644); err != nil { //nolint:gosec // G306: public certificates
			return fmt.Errorf("save original CA bundle: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("read %s: %w", originalCA, err)
	}
	content := orig
	if path != "" {
		extra, err := os.ReadFile(path) //nolint:gosec // G304: path from the host-written guest config
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return fmt.Errorf("read CA bundle %s: %w", path, err)
		case len(bytes.TrimSpace(extra)) > 0:
			content = append(append(append([]byte{}, orig...), '\n'), extra...)
		}
	}
	if current, err := os.ReadFile(systemCA); err == nil && bytes.Equal(current, content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(systemCA), 0o755); err != nil { //nolint:gosec // G301: system directory
		return fmt.Errorf("create %s: %w", filepath.Dir(systemCA), err)
	}
	tmp := systemCA + ".jailoc"
	if err := os.WriteFile(tmp, content, 0o644); err != nil { //nolint:gosec // G306: public certificates
		return fmt.Errorf("write CA bundle: %w", err)
	}
	if err := os.Rename(tmp, systemCA); err != nil {
		return fmt.Errorf("install CA bundle: %w", err)
	}
	return nil
}
