//go:build darwin || linux

package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Info describes a session. It is stored as <id>.json next to the session
// socket <id>.sock.
type Info struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Dir        string    `json:"dir"`
	Argv       []string  `json:"argv"`
	Env        []string  `json:"env,omitempty"`
	Rows       uint16    `json:"rows"`
	Cols       uint16    `json:"cols"`
	PID        int       `json:"pid,omitempty"`
	Created    time.Time `json:"created"`
	LastAttach time.Time `json:"last_attach"`
	Attached   bool      `json:"attached"`
}

func infoPath(stateDir, id string) string   { return filepath.Join(stateDir, id+".json") }
func socketPath(stateDir, id string) string { return filepath.Join(stateDir, id+".sock") }

func newID() (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 32 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func readInfo(stateDir, id string) (*Info, error) {
	if !validID(id) {
		return nil, fmt.Errorf("invalid session id %q", id)
	}
	data, err := os.ReadFile(infoPath(stateDir, id)) //nolint:gosec // G304: validated id inside the session directory
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("session %s does not exist", id)
		}
		return nil, fmt.Errorf("read session %s: %w", id, err)
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parse session %s: %w", id, err)
	}
	return &info, nil
}

func writeInfo(stateDir string, info *Info) error {
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("encode session %s: %w", info.ID, err)
	}
	tmp := infoPath(stateDir, info.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write session %s: %w", info.ID, err)
	}
	if err := os.Rename(tmp, infoPath(stateDir, info.ID)); err != nil {
		return fmt.Errorf("write session %s: %w", info.ID, err)
	}
	return nil
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// List returns the running sessions, most recently attached first. Entries
// of holders that no longer run are removed.
func List(stateDir string) ([]Info, error) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions: %w", err)
	}
	var out []Info
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !validID(id) {
			continue
		}
		info, err := readInfo(stateDir, id)
		if err != nil {
			continue
		}
		if info.PID == 0 {
			// Being created: the holder has not recorded itself yet.
			if time.Since(info.Created) < startTimeout {
				continue
			}
		} else if alive(info.PID) {
			out = append(out, *info)
			continue
		}
		_ = os.Remove(infoPath(stateDir, id))
		_ = os.Remove(socketPath(stateDir, id))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAttach.After(out[j].LastAttach) })
	return out, nil
}

// Find returns the most recently attached running session started in dir.
func Find(stateDir, dir string) (*Info, error) {
	infos, err := List(stateDir)
	if err != nil {
		return nil, err
	}
	for i := range infos {
		if infos[i].Dir == dir {
			return &infos[i], nil
		}
	}
	return nil, nil
}

// Stop ends a session: its program gets SIGHUP, then SIGKILL after a grace
// period.
func Stop(stateDir, id string) error {
	info, err := readInfo(stateDir, id)
	if err != nil {
		return err
	}
	if !alive(info.PID) {
		return fmt.Errorf("session %s is not running", id)
	}
	if err := syscall.Kill(info.PID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop session %s: %w", id, err)
	}
	return nil
}
