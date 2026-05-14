//go:build windows

package cmd

import (
	"errors"
	"os"
)

var sigWinch os.Signal // nil — PTY resize not supported on Windows

var errEIO = errors.New("input/output error")
