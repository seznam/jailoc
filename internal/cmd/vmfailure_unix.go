//go:build darwin || linux

package cmd

import (
	"cmp"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/seznam/jailoc/internal/vm"
)

const (
	// failureConsoleTail bounds how much of the guest console is scanned.
	failureConsoleTail = 8 << 10
	// failureLineMax caps every console line quoted in an error message.
	failureLineMax = 200
	// failureCauseLines and failureDiskLines cap the quoted console lines.
	failureCauseLines = 3
	failureDiskLines  = 4

	noSpaceLeft = "no space left on device"
	// dockerDataDir is where the guest mounts the Docker data disk.
	dockerDataDir = "/var/lib/docker"
)

// vmFailure explains why a VM did not come up, from its state, guest console
// or log. A full guest Docker disk gets its own explanation and remedy; an
// out-of-space error without concrete evidence of it (the failing path or the
// data disk's df) may be the guest root or the host filesystem.
func vmFailure(stateDir, logPath string) string {
	stateErr := ""
	if st, err := vm.ReadState(stateDir); err == nil {
		stateErr = st.Error
	}
	consolePath := filepath.Join(stateDir, "microvm", "console.log")
	console, consoleFound := readLogTail(consolePath, failureConsoleTail)
	diag := consoleDiagnostics(console, stateErr)
	switch {
	case hasNoSpace(console):
		return diskFullFailure(stateDir, stateErr, consolePath, diag, consoleFound, diag.dataFull)
	case hasNoSpace(stateErr):
		return diskFullFailure(stateDir, stateErr, consolePath, diag, consoleFound, false)
	}
	base := stateErr
	if base == "" {
		base = supervisorLogFailure(logPath)
	}
	if len(diag.startup) == 0 {
		return base
	}
	parts := []string{base}
	for _, line := range diag.startup {
		parts = append(parts, "guest: "+line)
	}
	parts = append(parts, "console log: "+consolePath)
	return strings.Join(parts, "; ")
}

// supervisorLogFailure summarizes the supervisor log: a known hint or its
// last lines.
func supervisorLogFailure(logPath string) string {
	data, err := os.ReadFile(logPath) //nolint:gosec // G304: jailoc-owned state path
	if err != nil || len(data) == 0 {
		return "see " + logPath
	}
	if strings.Contains(string(data), "IrqsExhausted") {
		return "the VM has more devices than libkrun can attach on this host (on x86_64 about 11 shared directories besides jailoc's own); remove some paths or mounts"
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	return strings.Join(lines, "; ")
}

func hasNoSpace(s string) bool {
	return strings.Contains(strings.ToLower(s), noSpaceLeft)
}

// readLogTail returns the last limit bytes of the file, starting at a line
// boundary, and whether the file could be read.
func readLogTail(path string, limit int64) (string, bool) {
	f, err := os.Open(path) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", false
	}
	truncated := info.Size() > limit
	if truncated {
		if _, err := f.Seek(info.Size()-limit, io.SeekStart); err != nil {
			return "", false
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return "", false
	}
	text := string(data)
	if truncated {
		// The first line is most likely cut in the middle.
		_, rest, found := strings.Cut(text, "\n")
		if !found {
			return "", true
		}
		text = rest
	}
	return text, true
}

// diskFullFailure describes an out-of-space failure: what the guest
// reported, where to look, and which setting grows the disk. guestEvidence
// tells whether the guest console shows the Docker data filesystem full; without
// that the error may come from the guest root filesystem or from the host
// filesystem that holds the disk images.
func diskFullFailure(stateDir, stateErr, consolePath string, diag consoleDiag, consoleFound, guestEvidence bool) string {
	parts := []string{}
	if stateErr != "" {
		parts = append(parts, stateErr)
	}
	for _, line := range diag.cause {
		parts = append(parts, "guest: "+line)
	}
	if guestEvidence {
		parts = append(parts, noSpaceLeft+" inside the guest: its Docker data filesystem is full, either of blocks or of inodes; this is the guest filesystem, not the host's free space")
	} else {
		parts = append(parts, noSpaceLeft+" was reported, but there is no guest console evidence of a full Docker data filesystem (a failing path under "+dockerDataDir+", or a df of it with no free blocks or inodes): it may be the guest root filesystem, which is backed by host storage, or the host filesystem holding the disk images, so check the host's free space too")
	}
	for _, line := range diag.df {
		parts = append(parts, "guest df: "+line)
	}

	consoleNote := consolePath
	if !consoleFound {
		consoleNote += " (not found)"
	}
	spec, err := vm.ReadSpec(stateDir)
	if err != nil {
		spec = nil
	}
	diskPath := filepath.Join(stateDir, "docker.img")
	diskNote := diskPath
	if spec != nil {
		diskNote += " (" + diskSizeLabel(cmp.Or(spec.DiskSize, int64(vm.DefaultDiskSize))) + " logical size)"
	}
	remedy := growthRemedy(spec)
	if !guestEvidence {
		remedy = "only if the console log shows the Docker data filesystem full: " + remedy
	}
	parts = append(parts,
		"console log: "+consoleNote+" (look for the guest df -k and df -i output there)",
		"data disk: "+diskNote,
		remedy+"; the disk can only grow and nothing is reset or deleted for you")
	return strings.Join(parts, "; ")
}

// consoleDiag holds the lines worth quoting from the guest console.
type consoleDiag struct {
	// cause: out-of-space, fatal, panic and init failure lines.
	cause []string
	// startup: only the guest's startup failures (init, kernel panic).
	startup []string
	// df: header and rows of the guest df listings.
	df []string
	// dataFull: a console line shows the Docker data filesystem full, either
	// an out-of-space error on a path under dockerDataDir or a df row of it
	// with no free blocks or inodes.
	dataFull bool
}

// consoleDiagnostics picks, from the console tail, the last fatal lines and
// the guest df lines, sanitized and length-capped. Lines equal to skip are left out.
func consoleDiagnostics(console, skip string) consoleDiag {
	var cause, startup, df []string
	dataFull := false
	for line := range strings.SplitSeq(console, "\n") {
		line = sanitizeConsoleLine(line)
		if line == "" {
			continue
		}
		if dataFilesystemFull(line) {
			dataFull = true
		}
		if line == skip {
			continue
		}
		if row, ok := dfLine(line); ok {
			df = append(df, row)
			continue
		}
		if isStartupFailure(line) {
			startup = append(startup, line)
		}
		if isFatalLine(line) {
			cause = append(cause, line)
		}
	}
	return consoleDiag{
		cause:    lastN(cause, failureCauseLines),
		startup:  lastN(startup, failureCauseLines),
		df:       lastN(df, failureDiskLines),
		dataFull: dataFull,
	}
}

// dataFilesystemFull reports whether one console line is concrete evidence
// that the Docker data filesystem is full: an out-of-space error whose failing
// path (the last word before the message) is dockerDataDir or below it, or a df
// row of dockerDataDir with 0 available blocks or inodes. A df row at 100%
// with space still available is rounding, not exhaustion.
func dataFilesystemFull(line string) bool {
	lower := strings.ToLower(line)
	if i := strings.Index(lower, noSpaceLeft); i >= 0 {
		words := strings.Fields(lower[:i])
		if len(words) == 0 {
			return false
		}
		path := strings.Trim(words[len(words)-1], `:,;"'`)
		return path == dockerDataDir || strings.HasPrefix(path, dockerDataDir+"/")
	}
	row, ok := dfLine(line)
	if !ok {
		return false
	}
	fields := strings.Fields(row)
	return len(fields) >= 6 && fields[3] == "0" && fields[len(fields)-1] == dockerDataDir
}

const initPrefix = "jailoc-init:"

// isStartupFailure matches a failure reported by the guest init or kernel.
func isStartupFailure(line string) bool {
	lower := strings.ToLower(line)
	if strings.Contains(lower, "kernel panic") {
		return true
	}
	return strings.HasPrefix(line, initPrefix) &&
		(strings.Contains(lower, "failed") || strings.Contains(lower, "unexpectedly"))
}

func isFatalLine(line string) bool {
	lower := strings.ToLower(line)
	return strings.Contains(lower, noSpaceLeft) || strings.Contains(lower, "fatal") ||
		strings.Contains(lower, "panic") || isStartupFailure(line)
}

// dfLine matches the header and the rows of a df listing of the data disk and
// returns them without the guest init's log prefix, which it puts in front of
// every df row.
func dfLine(line string) (string, bool) {
	row := strings.TrimSpace(strings.TrimPrefix(line, initPrefix))
	if strings.HasPrefix(row, "Filesystem") || strings.HasPrefix(row, "/dev/vd") {
		return row, true
	}
	return "", false
}

func lastN(lines []string, n int) []string {
	if len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

// sanitizeConsoleLine replaces control characters and caps the length.
func sanitizeConsoleLine(line string) string {
	line = strings.Map(func(r rune) rune {
		if r == '\t' || !unicode.IsPrint(r) {
			return ' '
		}
		return r
	}, strings.TrimRight(line, "\r"))
	line = strings.TrimSpace(line)
	if len(line) > failureLineMax {
		cut := failureLineMax
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		line = line[:cut] + "..."
	}
	return line
}

func diskSizeLabel(size int64) string {
	if size%(1<<30) == 0 {
		return fmt.Sprintf("%d GiB", size>>30)
	}
	return fmt.Sprintf("%d MiB", size>>20)
}

// growthRemedy names the setting that grows the Docker data disk of the VM
// described by spec (nil when its spec is unreadable).
func growthRemedy(spec *vm.Spec) string {
	const either = "raise [vm] builder_disk when the shared image builder failed, or docker_disk of the workspace otherwise"
	if spec == nil {
		return either
	}
	switch {
	case spec.Kind == vm.KindBuilder && spec.Workspace == "builder":
		return "raise [vm] builder_disk in the config"
	case spec.Kind == vm.KindBuilder:
		return fmt.Sprintf("raise docker_disk of workspace %q (its isolated image builder failed)", strings.TrimSuffix(spec.Workspace, "-builder"))
	case spec.Kind == vm.KindWorkspace:
		return fmt.Sprintf("raise docker_disk of workspace %q", spec.Workspace)
	}
	return either
}
