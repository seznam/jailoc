package docker

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	bkclient "github.com/moby/buildkit/client"
)

// TestLogBuildSteps swaps the default logger, so it does not run in
// parallel.
func TestLogBuildSteps(t *testing.T) {
	var out bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	start := time.Unix(1000, 0)
	done := start.Add(1500 * time.Millisecond)
	finished := map[string]bool{}
	logBuildSteps(&bkclient.SolveStatus{Vertexes: []*bkclient.Vertex{
		{Digest: "sha256:run", Name: "[2/3] RUN make", Started: &start},
	}}, finished)
	if out.Len() != 0 {
		t.Fatalf("logged a running step: %q", out.String())
	}
	status := &bkclient.SolveStatus{Vertexes: []*bkclient.Vertex{
		{Digest: "sha256:run", Name: "[2/3] RUN make", Started: &start, Completed: &done},
		{Digest: "sha256:copy", Name: "[3/3] COPY . .", Started: &start, Completed: &start, Cached: true},
		{Digest: "sha256:fail", Name: "[1/3] FROM base", Started: &start, Completed: &done, Error: "not found"},
	}}
	logBuildSteps(status, finished)
	logBuildSteps(status, finished) // updates repeat finished steps
	got := out.String()
	for _, want := range []string{
		`step="[2/3] RUN make" duration=1.5s cached=false`,
		`step="[3/3] COPY . ." duration=0s cached=true`,
		`step="[1/3] FROM base" duration=1.5s cached=false error="not found"`,
	} {
		if strings.Count(got, want) != 1 {
			t.Errorf("log %q does not hold %q once", got, want)
		}
	}
}
