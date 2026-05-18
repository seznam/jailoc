//go:build windows

package cmd

import (
	"errors"
	"os"
)

var sigWinch os.Signal // nil — no SIGWINCH on Windows; resize uses polling

var errEIO = errors.New("input/output error")

// terminateSignal is the signal sent to gracefully stop the child process.
// On Windows os.Interrupt sends CTRL_BREAK_EVENT to the child.
var terminateSignal os.Signal = os.Interrupt
