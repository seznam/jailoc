// Package agentstate prepares the persistent state directories of a workspace's
// coding agent on the host before its VM starts: it removes stale config
// mount points and sets the defaults jailoc owns, such as the status line.
package agentstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"

	"github.com/seznam/jailoc/internal/agent"
)

// guestHome is the agent's home directory in the guest.
const guestHome = "/home/agent"

// maxAgentState bounds the agent state files jailoc edits; Claude Code keeps
// per-project history in .claude.json.
const maxAgentState = 64 << 20

// maxAgentConfig bounds the agent settings files jailoc reads.
const maxAgentConfig = 1 << 20

// WorkspaceStatusLineCommand is the status line command of GitHub Copilot
// CLI and Antigravity CLI: it prints the workspace with the shell, so it
// needs no Node.js in the image.
const WorkspaceStatusLineCommand = `printf '🔒 jailoc / %s\n' "${JAILOC_WORKSPACE:-unknown}"`

// DirPrep describes how jailoc-guest prepares one persistent agent state
// directory on the state disk before sessions start.
type DirPrep struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// ScriptStatusLineCommand is the command that runs the jailoc status line
// script for harness. Claude Code runs it to chain the user's status line;
// GitHub Copilot CLI and Antigravity CLI settings may still hold it from
// an older jailoc.
func ScriptStatusLineCommand(harness string) string {
	return "node " + agent.PluginDir + "/statusline.mjs " + harness
}

// StatusLineCommand is the status line command of harness (claude, copilot
// or agy).
func StatusLineCommand(harness string) string {
	if harness == "claude" {
		return ScriptStatusLineCommand(harness)
	}
	return WorkspaceStatusLineCommand
}

// StatusLine is the statusLine setting of harness (claude, copilot or agy).
func StatusLine(harness string) map[string]string {
	return map[string]string{"type": "command", "command": StatusLineCommand(harness)}
}

// RemoveConfigMountPoints removes the empty files and directories the guest
// created in state directory d, at dir inside root, as mount points of
// import_agent_config snapshots. A snapshot that no longer applies, such as one
// turned off or one whose host file is gone, would otherwise leave the agent
// an empty settings file. The guest creates them again for the snapshots it
// binds.
func RemoveConfigMountPoints(root *os.Root, dir string, d agent.StateDir, h agent.Harness) error {
	for _, cf := range h.ConfigFiles {
		inner, ok := strings.CutPrefix(guestHome+"/"+cf.Path, d.Path+"/")
		if !ok {
			continue
		}
		p := path.Join(dir, inner)
		info, err := root.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect %s: %w", p, err)
		}
		switch {
		case info.Mode().IsRegular() && info.Size() == 0:
		case info.IsDir():
			entries, err := fs.ReadDir(root.FS(), p)
			if err != nil {
				return fmt.Errorf("read %s: %w", p, err)
			}
			if len(entries) > 0 {
				continue
			}
		default:
			continue
		}
		if err := root.Remove(p); err != nil {
			return fmt.Errorf("remove mount point %s: %w", p, err)
		}
	}
	return nil
}

// AddStateDefaults sets the StateDefaults of agent h that are in state
// directory d, at dir inside root. It runs only before the VM starts, while
// the agent cannot write the files. A failure leaves the agent without the
// defaults, so the failures are returned for the caller to report.
func AddStateDefaults(root *os.Root, dir string, d agent.StateDir, h agent.Harness) []error {
	var errs []error
	for _, sd := range h.StateDefaults {
		inner, ok := strings.CutPrefix(sd.File, d.Path+"/")
		if !ok {
			continue
		}
		if err := SetStateDefault(root, path.Join(dir, inner), sd); err != nil {
			errs = append(errs, fmt.Errorf("could not set %s in %s: %w", sd.Key, sd.File, err))
		}
	}
	return errs
}

// SetStateDefault sets the top-level key of sd in the JSON file inside root
// unless the file already has it.
func SetStateDefault(root *os.Root, file string, sd agent.StateDefault) error {
	values := map[string]json.RawMessage{}
	data, err := readRegular(root, file, maxAgentState)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &values); err != nil {
			return fmt.Errorf("parse %s: %w", path.Base(file), err)
		}
	}
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	if _, ok := values[sd.Key]; ok {
		return nil
	}
	values[sd.Key] = json.RawMessage(sd.Value)
	data, err = json.MarshalIndent(values, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path.Base(file), err)
	}
	if err := root.MkdirAll(path.Dir(file), 0o700); err != nil {
		return fmt.Errorf("create directory of %s: %w", file, err)
	}
	return replaceFile(root, file, append(data, '\n'))
}

// replaceFile writes data to file inside root through a temporary file that
// it syncs and renames over file, so a failed write (for example ENOSPC on a
// full state disk) leaves the old file intact.
func replaceFile(root *os.Root, file string, data []byte) error {
	tmp := file + ".jailoc-tmp"
	if err := root.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", tmp, err)
	}
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, file)
	}
	if err != nil {
		_ = root.Remove(tmp) // best effort; the write error is reported
		return fmt.Errorf("write %s: %w", file, err)
	}
	return nil
}

// AddStatusLine sets the jailoc defaults in the settings file of agent h in
// its state directory dir inside root: the status line (unless the file sets
// one other than the script status line of an older jailoc) and, for GitHub
// Copilot CLI, storeTokenPlaintext (when unset) so logins work without a
// system keyring in the VM. A failure leaves the agent without the defaults,
// so it is returned for the caller to report.
func AddStatusLine(root *os.Root, dir string, h agent.Harness) error {
	file, harness, ok := h.TUIPlugin.SettingsFile()
	if !ok {
		return nil
	}
	if err := SetDefaultStatusLine(root, path.Join(dir, file), harness); err != nil {
		return fmt.Errorf("add the jailoc status line to the %s settings: %w", h.Title, err)
	}
	return nil
}

// SetDefaultStatusLine sets the status line of harness, and for copilot
// storeTokenPlaintext, in the settings file inside root where they are unset.
func SetDefaultStatusLine(root *os.Root, file, harness string) error {
	settings := map[string]json.RawMessage{}
	data, err := readRegular(root, file, maxAgentConfig)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("parse %s: %w", path.Base(file), err)
		}
	}
	changed := false
	if old, ok := settings["statusLine"]; !ok || isLegacyStatusLine(old, harness) {
		line, err := json.Marshal(StatusLine(harness))
		if err != nil {
			return fmt.Errorf("encode status line: %w", err)
		}
		settings["statusLine"] = line
		changed = true
	}
	if harness == "copilot" {
		if _, ok := settings["storeTokenPlaintext"]; !ok {
			settings["storeTokenPlaintext"] = json.RawMessage("true")
			changed = true
		}
	}
	if !changed {
		return nil
	}
	data, err = json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path.Base(file), err)
	}
	if err := root.MkdirAll(path.Dir(file), 0o700); err != nil {
		return fmt.Errorf("create directory of %s: %w", file, err)
	}
	return replaceFile(root, file, append(data, '\n'))
}

func isLegacyStatusLine(raw json.RawMessage, harness string) bool {
	var line struct{ Command string }
	return json.Unmarshal(raw, &line) == nil && line.Command == ScriptStatusLineCommand(harness)
}

// readRegular reads name inside root when it is a regular file of at most
// maxSize bytes. The guest can write below root, so the file is opened
// without blocking (a FIFO would block a plain open) and read with a bound.
func readRegular(root *os.Root, name string, maxSize int64) ([]byte, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err // os.PathError already names the file; callers match fs.ErrNotExist
	}
	defer func() { _ = f.Close() }() // read-only
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	if info.Size() > maxSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, maxSize)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, maxSize)
	}
	return data, nil
}
