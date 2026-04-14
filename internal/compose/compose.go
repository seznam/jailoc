package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

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
	OpenCodePassword string
	Env              []string
	SSHAuthSock      string // host socket path to mount, empty = disabled
	SSHKnownHosts    string // host known_hosts path to mount (bound to SSHAuthSock), empty = disabled
	GitConfig        string // host gitconfig path to mount, empty = disabled
	CPU              float64
	Memory           string
	UseDataVolume    bool
	UseCacheVolume   bool
}

func GenerateCompose(params ComposeParams) ([]byte, error) {
	tmpl, err := template.New("docker-compose.yml").Funcs(template.FuncMap{
		"base": filepath.Base,
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

func MountsContainTarget(mounts []string, containerPath string) bool {
	for _, m := range mounts {
		parts := strings.SplitN(m, ":", 3)
		if len(parts) >= 2 && parts[1] == containerPath {
			return true
		}
	}
	return false
}
