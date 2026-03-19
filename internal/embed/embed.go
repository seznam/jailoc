// Package embed provides access to embedded Docker assets bundled with jailoc.
package embed

import _ "embed"

//go:embed assets/Dockerfile
var dockerfileBytes []byte

//go:embed assets/docker-compose.yml.tmpl
var composeTemplateStr string

//go:embed assets/entrypoint.sh
var entrypointBytes []byte

//go:embed assets/config.toml.default
var defaultConfigBytes []byte

// Dockerfile returns the embedded fallback Dockerfile bytes.
func Dockerfile() []byte { return dockerfileBytes }

// ComposeTemplate returns the embedded compose template string.
func ComposeTemplate() string { return composeTemplateStr }

// Entrypoint returns the embedded entrypoint.sh bytes.
func Entrypoint() []byte { return entrypointBytes }

// DefaultConfig returns the embedded default config template bytes.
func DefaultConfig() []byte { return defaultConfigBytes }
