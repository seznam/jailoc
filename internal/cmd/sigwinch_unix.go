//go:build !windows

package cmd

import (
	"os"
	"syscall"
)

var sigWinch os.Signal = syscall.SIGWINCH

var errEIO = syscall.EIO
