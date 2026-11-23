//go:build darwin

package vm

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// Seatbelt is applied with sandbox_init(3) from libSystem, without cgo:
// release builds use CGO_ENABLED=0. The call goes through an assembly
// trampoline (seatbelt_darwin_*.s) and the runtime's syscall.syscall, the way
// golang.org/x/sys/unix calls libSystem. The profile stays in force across
// exec, so a small shim process applies it to itself and then execs the VM
// runner (see RunSeatbeltShim).

// Implemented in the runtime package (runtime/sys_darwin.go).
//
//go:linkname syscall_syscall syscall.syscall
func syscall_syscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

var libc_sandbox_init_trampoline_addr uintptr

//go:cgo_import_dynamic libc_sandbox_init sandbox_init "/usr/lib/libSystem.B.dylib"

var libc_sandbox_free_error_trampoline_addr uintptr

//go:cgo_import_dynamic libc_sandbox_free_error sandbox_free_error "/usr/lib/libSystem.B.dylib"

// File descriptors the spawner passes to the shim as cmd.ExtraFiles.
const (
	shimProfileFD = 3 // ExtraFiles[0]: the SBPL profile
	shimErrorFD   = 4 // ExtraFiles[1]: error pipe, closed by exec on success
)

// applySeatbelt confines the calling process with the SBPL profile. Flags 0
// makes sandbox_init compile profile as SBPL source rather than look up a
// named profile.
func applySeatbelt(profile string) error {
	src, err := syscall.BytePtrFromString(profile)
	if err != nil {
		return fmt.Errorf("seatbelt profile: %w", err)
	}
	errbuf := new(*byte)
	r, _, _ := syscall_syscall(libc_sandbox_init_trampoline_addr,
		uintptr(unsafe.Pointer(src)), 0, uintptr(unsafe.Pointer(errbuf))) //nolint:gosec // G103: libSystem call through a trampoline
	runtime.KeepAlive(src)
	runtime.KeepAlive(errbuf)
	if int32(r) == 0 { //nolint:gosec // G115: sandbox_init returns a C int
		return nil
	}
	msg := "no error message"
	if *errbuf != nil {
		msg = cString(*errbuf)
		_, _, _ = syscall_syscall(libc_sandbox_free_error_trampoline_addr, uintptr(unsafe.Pointer(*errbuf)), 0, 0) //nolint:gosec // G103: libSystem call through a trampoline
	}
	return fmt.Errorf("sandbox_init: %s", msg)
}

func cString(p *byte) string {
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 { //nolint:gosec // G103: NUL-terminated string from libSystem
		n++
	}
	return string(unsafe.Slice(p, n)) //nolint:gosec // G103: NUL-terminated string from libSystem
}

// RunSeatbeltShim is the body of the hidden "vm-seatbelt" command. The
// spawner runs it as <runner-path> <process-name> <config-json> with the
// profile on fd 3 and an error pipe on fd 4. It applies the profile to
// itself and replaces itself with the runner, so the runner never runs
// unconfined: on any failure the reason goes to fd 4 and the runner is not
// started. A successful exec closes fd 4 (close-on-exec), which tells the
// spawner there was no error.
func RunSeatbeltShim(args []string) error {
	syscall.CloseOnExec(shimErrorFD)
	fail := func(err error) error {
		errPipe := os.NewFile(shimErrorFD, "seatbelt-error-pipe")
		_, _ = fmt.Fprintln(errPipe, err.Error()) // best effort: the returned error also reaches the log
		_ = errPipe.Close()
		return err
	}

	if len(args) != 3 {
		return fail(fmt.Errorf("vm-seatbelt: want <runner-path> <process-name> <config-json>, got %d arguments", len(args)))
	}
	runnerPath, processName, configJSON := args[0], args[1], args[2]

	profileFile := os.NewFile(shimProfileFD, "seatbelt-profile")
	profile, err := io.ReadAll(profileFile)
	_ = profileFile.Close() // read side of a pipe; nothing to flush
	if err != nil {
		return fail(fmt.Errorf("vm-seatbelt: read profile: %w", err))
	}
	if err := applySeatbelt(string(profile)); err != nil {
		return fail(fmt.Errorf("vm-seatbelt: apply profile: %w", err))
	}

	argv := []string{processName, configJSON}
	if processName == "" {
		argv[0] = runnerPath
	}
	if err := syscall.Exec(runnerPath, argv, os.Environ()); err != nil { //nolint:gosec // G204: the spawner passes the runner path it resolved
		return fail(fmt.Errorf("vm-seatbelt: exec %s: %w", runnerPath, err))
	}
	return nil // not reached: Exec replaced the process
}
