// Package embed provides access to embedded Docker assets bundled with jailoc.
package embed

import _ "embed"

//go:embed assets/Dockerfile
var dockerfileBytes []byte

//go:embed assets/docker-compose.yml.tmpl
var composeTemplateStr string

//go:embed assets/entrypoint.sh
var entrypointBytes []byte

//go:embed assets/dind-entrypoint.sh
var dindEntrypointBytes []byte

//go:embed assets/tui.js
var tuiPluginJS []byte

//go:embed assets/tui-plugin.json
var tuiPluginJSON []byte

// Dockerfile returns the embedded fallback Dockerfile bytes.
func Dockerfile() []byte { return dockerfileBytes }

// ComposeTemplate returns the embedded compose template string.
func ComposeTemplate() string { return composeTemplateStr }

// Entrypoint returns the embedded entrypoint.sh bytes.
func Entrypoint() []byte { return entrypointBytes }

// DindEntrypoint returns the embedded dind-entrypoint.sh bytes.
func DindEntrypoint() []byte { return dindEntrypointBytes }

// TUIPluginJS returns the embedded TUI plugin JavaScript bytes.
func TUIPluginJS() []byte { return tuiPluginJS }

// TUIPluginJSON returns the embedded TUI plugin package.json bytes.
func TUIPluginJSON() []byte { return tuiPluginJSON }
