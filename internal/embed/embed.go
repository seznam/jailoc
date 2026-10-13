// Package embed provides access to bundled workspace image and TUI assets.
package embed

import _ "embed"

//go:embed assets/Dockerfile
var dockerfileBytes []byte

//go:embed assets/docker.Dockerfile
var dockerLayerBytes []byte

//go:embed assets/tui-opencode1.js
var tuiPluginOpenCode1JS []byte

//go:embed assets/tui-opencode2.js
var tuiPluginOpenCode2JS []byte

//go:embed assets/tui-plugin.json
var tuiPluginJSON []byte

//go:embed assets/omo.js
var omoExtensionJS []byte

//go:embed assets/statusline.mjs
var statusLineJS []byte

// Dockerfile returns the embedded fallback Dockerfile bytes.
func Dockerfile() []byte { return dockerfileBytes }

// DockerLayer returns the stages of the embedded Docker layer Dockerfile;
// they leave the files to copy onto the base image in the docker stage's /out.
func DockerLayer() []byte { return dockerLayerBytes }

// TUIPluginOpenCode1JS returns the embedded TUI plugin JavaScript for
// OpenCode 1.
func TUIPluginOpenCode1JS() []byte { return tuiPluginOpenCode1JS }

// TUIPluginOpenCode2JS returns the embedded TUI plugin JavaScript for
// OpenCode 2.
func TUIPluginOpenCode2JS() []byte { return tuiPluginOpenCode2JS }

// TUIPluginJSON returns the embedded TUI plugin package.json bytes.
func TUIPluginJSON() []byte { return tuiPluginJSON }

// OmOExtensionJS returns the embedded OmO extension.
func OmOExtensionJS() []byte { return omoExtensionJS }

// StatusLineJS returns the embedded status line script of Claude Code and
// GitHub Copilot CLI.
func StatusLineJS() []byte { return statusLineJS }
