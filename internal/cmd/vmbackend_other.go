//go:build !darwin && !linux

package cmd

import (
	"context"
	"errors"
	"io"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

var errVMUnsupported = errors.New(`backend "vm" is supported on macOS and Linux only`)

func runUpVM(context.Context, *config.Config, *workspace.Resolved) error { return errVMUnsupported }

func runSessionVM(context.Context, *config.Config, *workspace.Resolved, vmSessionOptions) error {
	return errVMUnsupported
}

func listSessionsVM(context.Context, *workspace.Resolved) error { return errVMUnsupported }

func stopSessionVM(context.Context, *workspace.Resolved, string) error { return errVMUnsupported }

func downVM(*workspace.Resolved, bool) error { return errVMUnsupported }

func statusVM(*workspace.Resolved) error { return errVMUnsupported }

func logsVM(context.Context, *workspace.Resolved, bool, io.Writer) error { return errVMUnsupported }

func restartVMIfRunning(context.Context, *workspace.Resolved) error { return errVMUnsupported }
