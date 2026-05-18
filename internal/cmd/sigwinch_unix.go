//go:build !windows

package cmd

import (
	"os"
	"syscall"
)

var sigWinch os.Signal = syscall.SIGWINCH

var errEIO = syscall.EIO

// terminateSignal is the signal sent to gracefully stop the child process.
var terminateSignal os.Signal = syscall.SIGTERM
