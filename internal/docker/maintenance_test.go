package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	dockerclient "github.com/moby/moby/client"
)

type recordedRequest struct {
	method, path string
	query        url.Values
}

type builderDaemon struct {
	mu       sync.Mutex
	requests []recordedRequest
	// images is the ImageList reply; layers maps an image ID to its RootFS layers.
	images []map[string]any
	layers map[string][]string
	// containers is the ContainerList reply.
	containers []map[string]any
	// failPrune, failContainers and failRemove make the matching requests fail.
	failPrune      bool
	failContainers bool
	failRemove     func(ref string) bool
}

func (d *builderDaemon) handler(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.requests = append(d.requests, recordedRequest{r.Method, r.URL.Path, r.URL.Query()})
	d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	if i := strings.Index(path, "/v1."); i >= 0 {
		path = path[i:]
		path = path[strings.Index(path[1:], "/")+1:]
	}
	switch {
	case path == "/_ping":
		w.Header().Set("API-Version", "1.52")
		w.Header().Set("OSType", "linux")
		_, _ = w.Write([]byte("OK"))
	case r.Method == http.MethodPost && path == "/build/prune":
		if d.failPrune {
			http.Error(w, `{"message":"prune failed"}`, http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"CachesDeleted":["c1","c2"],"SpaceReclaimed":1234}`))
	case r.Method == http.MethodGet && path == "/containers/json":
		if d.failContainers {
			http.Error(w, `{"message":"containers unavailable"}`, http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(d.containers)
	case r.Method == http.MethodGet && path == "/images/json":
		_ = json.NewEncoder(w).Encode(d.images)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/images/"), "/json")
		layers, ok := d.layers[id]
		if !ok {
			http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "RootFS": map[string]any{"Type": "layers", "Layers": layers}})
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/images/"):
		ref := strings.TrimPrefix(path, "/images/")
		if d.failRemove != nil && d.failRemove(ref) {
			http.Error(w, `{"message":"conflict: image is being used"}`, http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{{"Untagged": ref}})
	default:
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}
}

func (d *builderDaemon) client(t *testing.T) *dockerclient.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(d.handler))
	t.Cleanup(ts.Close)
	cli, err := dockerclient.New(dockerclient.WithHost("tcp://"+strings.TrimPrefix(ts.URL, "http://")), dockerclient.WithAPIVersion("1.52"))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func (d *builderDaemon) find(method, prefix string) []recordedRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []recordedRequest
	for _, r := range d.requests {
		if r.method == method && strings.Contains(r.path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

func hexID(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func summary(id string, age time.Duration, now time.Time, tags ...string) map[string]any {
	return map[string]any{
		"Id":       id,
		"RepoTags": tags,
		"Created":  now.Add(-age).Unix(),
		"Labels":   map[string]string{ImageLabel: "1"},
	}
}

func containerSummary(id, imageID, state string) map[string]any {
	return map[string]any{"Id": id, "Image": imageID, "ImageID": imageID, "State": state}
}

// removedRefs returns the references of the DELETE /images requests, failing
// the test for any forced or child-pruning one.
func (d *builderDaemon) removedRefs(t *testing.T) []string {
	t.Helper()
	var removed []string
	for _, r := range d.find(http.MethodDelete, "/images/") {
		if r.query.Has("force") {
			t.Errorf("DELETE %s forced removal: %v", r.path, r.query)
		}
		if r.query.Get("noprune") != "1" {
			t.Errorf("DELETE %s lacks noprune=1, the daemon would remove untagged parents: %v", r.path, r.query)
		}
		removed = append(removed, r.path[strings.LastIndex(r.path, "/images/")+len("/images/"):])
	}
	return removed
}

func TestMaintainBuilderRequests(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	unlabeled := summary(hexID("9"), 72*time.Hour, now, "jailoc-unlabeled:old")
	delete(unlabeled, "Labels")
	d := &builderDaemon{
		images: []map[string]any{
			summary(hexID("1"), 48*time.Hour, now, "jailoc-agent-omo:kept"),                           // in the EROFS cache
			summary(hexID("2"), 30*time.Minute, now, "jailoc-agent-omo:recent"),                       // inside the grace period
			summary(hexID("3"), 48*time.Hour, now, "jailoc-ws:foreign", "registry.example/team/ws:1"), // foreign tag
			summary(hexID("4"), 48*time.Hour, now, "jailoc-base:embedded"),                            // ancestor of the kept image
			summary(hexID("5"), 48*time.Hour, now, "jailoc-agent-omo:stale"),                          // removable
			summary(hexID("6"), 48*time.Hour, now),                                                    // untagged, removable
			unlabeled,
		},
		layers: map[string][]string{
			hexID("1"): {"l1", "l2", "l3"},
			hexID("2"): {"r1"},
			hexID("3"): {"f1"},
			hexID("4"): {"l1", "l2"},
			hexID("5"): {"s1"},
			hexID("6"): {"u1"},
		},
	}
	const disk = int64(20 << 30)
	got, err := maintainBuilder(context.Background(), d.client(t), disk, map[string]bool{strings.Repeat("1", 64): true}, now)
	if err != nil {
		t.Fatalf("maintainBuilder: %v", err)
	}

	prune := d.find(http.MethodPost, "/build/prune")
	if len(prune) != 1 {
		t.Fatalf("build prune requests = %d, want 1", len(prune))
	}
	q := prune[0].query
	if q.Get("all") != "1" || q.Get("max-used-space") != "10737418240" || q.Get("min-free-space") != "4294967296" {
		t.Errorf("build prune query = %v, want all=1 max-used-space=disk/2 min-free-space=disk/5", q)
	}
	for _, k := range []string{"reserved-space", "keep-storage"} {
		if q.Has(k) {
			t.Errorf("build prune query has %s, want ReservedSpace zero", k)
		}
	}

	list := d.find(http.MethodGet, "/images/json")
	if len(list) != 1 || !strings.Contains(list[0].query.Get("filters"), ImageLabel) {
		t.Fatalf("image list requests = %+v, want one filtered by %s", list, ImageLabel)
	}

	if list[0].query.Get("all") != "1" {
		t.Errorf("image list query = %v, want all=1 to enumerate untagged intermediate images", list[0].query)
	}
	cl := d.find(http.MethodGet, "/containers/json")
	if len(cl) != 1 || cl[0].query.Get("all") != "1" {
		t.Errorf("container list requests = %+v, want one with all=1", cl)
	}
	removed := d.removedRefs(t)
	want := []string{"jailoc-agent-omo:stale", hexID("6")}
	if strings.Join(removed, ",") != strings.Join(want, ",") {
		t.Errorf("removed = %v, want %v", removed, want)
	}
	if got.CacheReclaimed != 1234 || got.CachesDeleted != 2 || got.ImagesRemoved != 2 {
		t.Errorf("result = %+v, want 1234 bytes, 2 caches, 2 images", got)
	}
}

func TestMaintainBuilderReportsEveryFailure(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	d := &builderDaemon{
		failPrune: true,
		images: []map[string]any{
			summary(hexID("5"), 48*time.Hour, now, "jailoc-agent-omo:busy"),
			summary(hexID("6"), 48*time.Hour, now, "jailoc-agent-omo:stale"),
		},
		layers:     map[string][]string{hexID("5"): {"s1"}, hexID("6"): {"u1"}},
		failRemove: func(ref string) bool { return ref == "jailoc-agent-omo:busy" },
	}
	got, err := maintainBuilder(context.Background(), d.client(t), 20<<30, nil, now)
	if err == nil {
		t.Fatal("maintainBuilder error = nil, want the failures")
	}
	for _, want := range []string{"prune build cache", "remove image jailoc-agent-omo:busy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if got.ImagesRemoved != 1 {
		t.Errorf("ImagesRemoved = %d, want 1: a failed removal must not stop the others", got.ImagesRemoved)
	}
}

func TestMaintainBuilderRejectsNonPositiveDisk(t *testing.T) {
	t.Parallel()
	d := &builderDaemon{}
	if _, err := maintainBuilder(context.Background(), d.client(t), 0, nil, time.Now()); err == nil {
		t.Fatal("maintainBuilder with zero disk = nil error")
	}
	if n := len(d.find(http.MethodPost, "/build/prune")); n != 0 {
		t.Errorf("build prune requests = %d, want 0", n)
	}
}

func TestMaintainBuilderProtectsContainerImages(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	// The summaries keep Containers at -1 as the daemon does for a plain list.
	runningTagged := summary(hexID("5"), 48*time.Hour, now, "jailoc-agent-omo:run-a", "jailoc-agent-omo:run-b")
	stoppedUntagged := summary(hexID("6"), 48*time.Hour, now)
	for _, img := range []map[string]any{runningTagged, stoppedUntagged} {
		img["Containers"] = -1
	}
	stale := summary(hexID("7"), 48*time.Hour, now, "jailoc-agent-omo:stale")
	stale["Containers"] = -1
	d := &builderDaemon{
		images: []map[string]any{
			runningTagged,
			stoppedUntagged,
			stale,
			summary(hexID("8"), 48*time.Hour, now, "jailoc-base:ancestor"),      // ancestor of the running image
			summary(hexID("b"), 48*time.Hour, now, "jailoc-base:unlisted-base"), // ancestor of an unlisted container image
		},
		layers: map[string][]string{
			hexID("5"): {"a1", "a2", "a3"},
			hexID("6"): {"b1"},
			hexID("7"): {"s1"},
			hexID("8"): {"a1", "a2"},
			hexID("a"): {"x1", "x2"},
			hexID("b"): {"x1"},
		},
		containers: []map[string]any{
			containerSummary("c1", hexID("5"), "running"),
			containerSummary("c2", hexID("6"), "exited"),
			containerSummary("c3", hexID("a"), "created"), // image outside the jailoc label filter
		},
	}
	got, err := maintainBuilder(context.Background(), d.client(t), 20<<30, nil, now)
	if err != nil {
		t.Fatalf("maintainBuilder: %v", err)
	}
	removed := d.removedRefs(t)
	if want := []string{"jailoc-agent-omo:stale"}; strings.Join(removed, ",") != strings.Join(want, ",") {
		t.Errorf("removed = %v, want %v", removed, want)
	}
	if got.ImagesRemoved != 1 {
		t.Errorf("ImagesRemoved = %d, want 1", got.ImagesRemoved)
	}
}

func TestMaintainBuilderContainerListFailureRemovesNoImage(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	d := &builderDaemon{
		failContainers: true,
		images:         []map[string]any{summary(hexID("5"), 48*time.Hour, now, "jailoc-agent-omo:stale")},
		layers:         map[string][]string{hexID("5"): {"s1"}},
	}
	got, err := maintainBuilder(context.Background(), d.client(t), 20<<30, nil, now)
	if err == nil || !strings.Contains(err.Error(), "list containers") {
		t.Fatalf("maintainBuilder error = %v, want a list containers failure", err)
	}
	if n := len(d.find(http.MethodDelete, "/images/")); n != 0 {
		t.Errorf("DELETE image requests = %d, want 0 when containers cannot be listed", n)
	}
	if n := len(d.find(http.MethodPost, "/build/prune")); n != 1 {
		t.Errorf("build prune requests = %d, want 1: cache maintenance still runs", n)
	}
	if got.CacheReclaimed != 1234 || got.ImagesRemoved != 0 {
		t.Errorf("result = %+v, want the cache reclaimed and no image removed", got)
	}
}

func TestMaintainBuilderKeepsUntaggedKeptAndUnlabeledParents(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	unlabeledParent := summary(hexID("4"), 72*time.Hour, now)
	delete(unlabeledParent, "Labels")
	d := &builderDaemon{
		images: []map[string]any{
			summary(hexID("1"), 48*time.Hour, now), // untagged, kept by the EROFS cache
			unlabeledParent,                        // parent without the jailoc label
			summary(hexID("5"), 48*time.Hour, now, "jailoc-agent-omo:old"), // selected for removal
		},
		layers: map[string][]string{hexID("1"): {"k1"}, hexID("5"): {"k2", "k3"}},
	}
	if _, err := maintainBuilder(context.Background(), d.client(t), 20<<30, map[string]bool{hexID("1"): true}, now); err != nil {
		t.Fatalf("maintainBuilder: %v", err)
	}
	if removed, want := d.removedRefs(t), []string{"jailoc-agent-omo:old"}; strings.Join(removed, ",") != strings.Join(want, ",") {
		t.Errorf("removed = %v, want %v", removed, want)
	}
}
