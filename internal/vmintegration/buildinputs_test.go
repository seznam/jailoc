//go:build vmintegration

package vmintegration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBuildInputAdmission checks that `jailoc up` refuses, before it builds
// or boots anything, a workspace whose read-write paths expose its
// Dockerfile, and that the error names the input and the share.
func TestBuildInputAdmission(t *testing.T) {
	dockerfile := filepath.Join(project, "Dockerfile")
	writeTmpConfig(t, tmpWorkspace{Settings: fmt.Sprintf("dockerfile = %q", dockerfile)})
	if err := os.WriteFile(dockerfile, []byte("ARG BASE\nFROM ${BASE}\n"), 0o600); err != nil { //nolint:gosec // G703: test directory
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(dockerfile) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := tmpJailoc(ctx, "up", workspaceName)
	if err == nil {
		t.Fatalf("jailoc up accepted a Dockerfile in a read-write path:\n%s", tail([]byte(out), 2000))
	}
	for _, want := range []string{
		fmt.Sprintf("Dockerfile %s of workspace %q lies in the read-write share %s", dockerfile, workspaceName, project),
		"mount them read-only",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("jailoc up output does not contain %q:\n%s", want, tail([]byte(out), 2000))
		}
	}
	if strings.Contains(out, "Building") || strings.Contains(out, "Starting VM") {
		t.Errorf("jailoc up built or started before refusing:\n%s", tail([]byte(out), 2000))
	}
	pids, err := tmpRunnerPIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) != 0 {
		t.Errorf("VM runner of the refused workspace is running: %v", pids)
	}
}
