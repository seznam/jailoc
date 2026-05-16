package compose

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/compose-spec/compose-go/v2/format"
	"github.com/seznam/jailoc/internal/embed"
)

type ComposeParams struct {
	WorkspaceName    string
	Port             int
	Image            string
	Paths            []string
	Mounts           []string
	AllowedHosts     []string
	AllowedNetworks  []string
	Env []string
	SSHAuthSock      string // host socket path to mount, empty = disabled
	SSHKnownHosts    string // host known_hosts path to mount (bound to SSHAuthSock), empty = disabled
	GitConfig        string // host gitconfig path to mount, empty = disabled
	CPU              float64
	Memory           string
	UseDataVolume    bool
	UseCacheVolume   bool
	ExposePort       bool
	EnableDocker     bool
}

func GenerateCompose(params ComposeParams) ([]byte, error) {
	tmpl, err := template.New("docker-compose.yml").Funcs(template.FuncMap{
		"base":          filepath.Base,
		"yamlQuote":     yamlQuote,
		"containerPath": containerPath,
	}).Parse(embed.ComposeTemplate())
	if err != nil {
		return nil, fmt.Errorf("parse compose template: %w", err)
	}

	var out strings.Builder
	if err := tmpl.Execute(&out, params); err != nil {
		return nil, fmt.Errorf("render compose template: %w", err)
	}

	return []byte(out.String()), nil
}

func yamlQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// containerPath converts a host filesystem path to a Linux container path.
// On Unix hosts the path is already valid; on Windows it converts
// backslashes to forward slashes and turns "C:\foo" into "/C/foo".
// Only absolute drive paths (e.g. C:\foo, D:/bar) are rewritten;
// drive-relative paths like "C:foo" are left unchanged.
func containerPath(hostPath string) string {
	p := strings.ReplaceAll(hostPath, `\`, "/")
	if len(p) >= 3 && p[1] == ':' && p[2] == '/' &&
		((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z')) {
		p = "/" + string(p[0]) + p[2:]
	}
	return p
}

// splitMountSpec splits a mount spec string (host:container[:mode]) into its
// parts, handling Windows drive-letter prefixes via format.ParseVolume.
func splitMountSpec(spec string) (host, container, mode string, ok bool) {
	// format.ParseVolume rejects empty source sections;
	// handle removal specs (e.g. ":/container:ro") separately.
	if strings.HasPrefix(spec, ":") {
		rest := spec[1:]
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) == 0 || parts[0] == "" {
			return "", "", "", false
		}
		m := "rw"
		if len(parts) == 2 {
			m = parts[1]
		}
		return "", parts[0], m, true
	}

	vol, err := format.ParseVolume(spec)
	if err != nil || vol.Target == "" {
		return "", "", "", false
	}

	m := "rw"
	if vol.ReadOnly {
		m = "ro"
	}
	return vol.Source, vol.Target, m, true
}

func WriteComposeFile(params ComposeParams, destPath string) error {
	composeBytes, err := GenerateCompose(params)
	if err != nil {
		return fmt.Errorf("generate compose file content: %w", err)
	}

	if err := os.WriteFile(destPath, composeBytes, 0o600); err != nil {
		return fmt.Errorf("write compose file to %q: %w", destPath, err)
	}

	return nil
}

func MountsContainTarget(mounts []string, target string) bool {
	for _, m := range mounts {
		_, container, _, ok := splitMountSpec(m)
		if ok && container == target {
			return true
		}
	}
	return false
}

func ReadOnlyMountCoversPath(mounts []string, target string) (hostPath string, found bool) {
	var bestHost, bestTarget, bestMode string
	bestLen := -1

	for _, m := range mounts {
		host, container, mode, ok := splitMountSpec(m)
		if !ok {
			continue
		}

		if container != target && !strings.HasPrefix(target, container+"/") {
			continue
		}
		if len(container) > bestLen {
			bestHost = host
			bestTarget = container
			bestMode = mode
			bestLen = len(container)
		}
	}

	if bestLen < 0 || bestMode != "ro" {
		return "", false
	}
	if bestTarget == target {
		return bestHost, true
	}
	suffix := target[len(bestTarget):]
	return bestHost + suffix, true
}
