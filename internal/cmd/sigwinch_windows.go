//go:build windows

package cmd

import (
	"errors"
	"os"
)

var sigWinch os.Signal // nil — no SIGWINCH on Windows; resize uses polling

var errEIO = errors.New("input/output error")

// terminateSignal is the signal used to stop the child process.
// On Windows os.Interrupt is not supported by os.Process.Signal, so we use
// os.Kill to terminate the process directly.
var terminateSignal os.Signal = os.Kill
