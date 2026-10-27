// Package embed provides access to bundled workspace image and TUI assets.
package embed

import _ "embed"

//go:embed assets/Dockerfile
var dockerfileBytes []byte

//go:embed assets/tui.js
var tuiPluginJS []byte

//go:embed assets/tui-plugin.json
var tuiPluginJSON []byte

// Dockerfile returns the embedded fallback Dockerfile bytes.
func Dockerfile() []byte { return dockerfileBytes }

// TUIPluginJS returns the embedded TUI plugin JavaScript bytes.
func TUIPluginJS() []byte { return tuiPluginJS }

// TUIPluginJSON returns the embedded TUI plugin package.json bytes.
func TUIPluginJSON() []byte { return tuiPluginJSON }
