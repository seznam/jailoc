package docker

import (
	"slices"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
)

func TestPruneCandidates(t *testing.T) {
	t.Parallel()
	now := time.Now()
	old := now.Add(-2 * time.Hour).Unix()
	images := []image.Summary{
		{ID: "kept", RepoTags: []string{"jailoc-base:embedded"}, Created: old},
		{ID: "stale", RepoTags: []string{"jailoc-agent-omo:aaaa"}, Created: old},
		{ID: "two-tags", RepoTags: []string{"jailoc-ws:1", "jailoc-ws:2"}, Created: old},
		{ID: "dangling", RepoTags: []string{"<none>:<none>"}, Created: old},
		{ID: "untagged", Created: old},
		{ID: "user", RepoTags: []string{"jailoc-base:old", "mine:latest"}, Created: old},
		{ID: "fresh", RepoTags: []string{"jailoc-docker:bbbb"}, Created: now.Unix()},
		{ID: "stale", RepoTags: []string{"jailoc-agent-omo:aaaa"}, Created: old},
	}
	got := pruneCandidates(images, map[string]bool{"kept": true}, now.Add(-time.Hour))
	want := []string{"jailoc-agent-omo:aaaa", "jailoc-ws:1", "jailoc-ws:2", "dangling", "untagged"}
	if !slices.Equal(got, want) {
		t.Errorf("pruneCandidates() = %v, want %v", got, want)
	}
}
