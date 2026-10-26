//go:build !darwin && !linux

package cmd

import (
	"context"
	"errors"

	"github.com/seznam/jailoc/internal/compose"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

var errVMUnsupported = errors.New(`backend "vm" is supported on macOS and Linux only`)

func connectBackend(ws *workspace.Resolved) (bool, error) {
	if ws.Backend != config.BackendVM {
		return true, nil
	}
	return false, errVMUnsupported
}

func startVM(context.Context, *config.Config, *workspace.Resolved) error { return errVMUnsupported }

func stopVM(string, bool) error { return errVMUnsupported }

func vmComposeParams(*compose.ComposeParams, string) error { return errVMUnsupported }
