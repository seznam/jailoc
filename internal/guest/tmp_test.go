package guest

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTmpConfigResolve(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      TmpConfig
		want    TmpConfig
		wantErr bool
	}{
		{name: "zero config keeps the legacy tmpfs", want: TmpConfig{Storage: TmpStorageRAM, SizeBytes: 1 << 30}},
		{
			name: "host defaults",
			in:   TmpConfig{Storage: TmpStorageRAM, SizeBytes: 1 << 30, TTL: 8 * time.Hour},
			want: TmpConfig{Storage: TmpStorageRAM, SizeBytes: 1 << 30, TTL: 8 * time.Hour},
		},
		{
			name: "disk",
			in:   TmpConfig{Storage: TmpStorageDisk, SizeBytes: 10 << 30, TTL: time.Hour},
			want: TmpConfig{Storage: TmpStorageDisk, SizeBytes: 10 << 30, TTL: time.Hour},
		},
		{name: "ram size only", in: TmpConfig{SizeBytes: 512 << 20}, want: TmpConfig{Storage: TmpStorageRAM, SizeBytes: 512 << 20}},
		{name: "unknown storage", in: TmpConfig{Storage: "nvme"}, wantErr: true},
		{name: "negative size", in: TmpConfig{SizeBytes: -1}, wantErr: true},
		{name: "negative ttl", in: TmpConfig{TTL: -time.Second}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.in.resolve()
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolve() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("resolve() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTmpEnv(t *testing.T) {
	t.Parallel()
	all := []string{"TMPDIR=/tmp", "TMP=/tmp", "TEMP=/tmp", "GOTMPDIR=/tmp"}
	tests := []struct {
		name string
		env  []string
		want []string
	}{
		{name: "all unset", env: []string{"PATH=/bin"}, want: all},
		{name: "image sets TMPDIR", env: []string{"TMPDIR=/scratch"}, want: all[1:]},
		{name: "empty value counts as set", env: []string{"TEMP=", "GOTMPDIR="}, want: all[:2]},
		{name: "everything set", env: []string{"TMPDIR=/a", "TMP=/b", "TEMP=/c", "GOTMPDIR=/d"}},
		{name: "similar names do not count", env: []string{"TMPDIRS=/x", "XTMP=/y"}, want: all},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tmpEnv(tt.env); !slices.Equal(got, tt.want) {
				t.Fatalf("tmpEnv(%q) = %q, want %q", tt.env, got, tt.want)
			}
		})
	}
}

// TestTmpEnvPrecedence follows a session environment the way programEnv
// builds it: the base (image, configured entries, defaults), then the
// secrets of EnvFile. Image, configured and secret values beat the defaults.
func TestTmpEnvPrecedence(t *testing.T) {
	t.Parallel()
	base := []string{"PATH=/bin", "TMP=/image-tmp"}
	base = append(base, "TEMP=/configured")
	base = append(base, tmpEnv(base)...)
	secrets := []string{"GOTMPDIR=/secret"}
	env := lastWins(append(slices.Clone(base), secrets...))
	want := map[string]string{"TMPDIR": "/tmp", "TMP": "/image-tmp", "TEMP": "/configured", "GOTMPDIR": "/secret"}
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, ok := want[k]; ok {
			got[k] = v
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (env %q)", k, got[k], v, env)
		}
	}
}

func TestTmpScratchDisk(t *testing.T) {
	t.Parallel()
	if got := tmpScratchDisk(&Config{}); got != "/dev/vda" {
		t.Fatalf("without Docker: %q, want /dev/vda", got)
	}
	if got := tmpScratchDisk(&Config{Docker: &Docker{Disk: DockerDisk}}); got != "/dev/vdb" {
		t.Fatalf("with Docker: %q, want /dev/vdb", got)
	}
}

func TestTmpMkfsArgs(t *testing.T) {
	t.Parallel()
	want := []string{"-q", "-F", "-m", "0", "-E", "nodiscard", "/dev/vdb"}
	if got := tmpMkfsArgs("/dev/vdb"); !slices.Equal(got, want) {
		t.Fatalf("tmpMkfsArgs = %q, want %q", got, want)
	}
}

func TestTmpUsage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                       string
		blocks, bfree, bavail, bsz uint64
		want                       TmpUsage
	}{
		{name: "empty", blocks: 100, bfree: 100, bavail: 100, bsz: 4096, want: TmpUsage{CapacityBytes: 409600, AvailableBytes: 409600}},
		{name: "reserved blocks", blocks: 100, bfree: 40, bavail: 30, bsz: 1024, want: TmpUsage{CapacityBytes: 102400, UsedBytes: 61440, AvailableBytes: 30720}},
		{name: "inconsistent free count", blocks: 10, bfree: 20, bavail: 20, bsz: 1, want: TmpUsage{CapacityBytes: 10, AvailableBytes: 20}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tmpUsage(tt.blocks, tt.bfree, tt.bavail, tt.bsz); got != tt.want {
				t.Fatalf("tmpUsage = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTmpJSONContract(t *testing.T) {
	t.Parallel()
	if Protocol != 4 {
		t.Fatalf("Protocol = %d, want 4", Protocol)
	}
	cfg, err := json.Marshal(Config{Tmp: TmpConfig{Storage: TmpStorageDisk, SizeBytes: 5, TTL: 2 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `"tmp":{"storage":"disk","size_bytes":5,"ttl_ns":2000000000}`; !strings.Contains(string(cfg), want) {
		t.Fatalf("config JSON %s lacks %s", cfg, want)
	}
	plain, err := json.Marshal(Reload{Seq: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "tmp_ttl_ns") {
		t.Fatalf("reload without TmpTTL marshals it: %s", plain)
	}
	zero := time.Duration(0)
	off, err := json.Marshal(Reload{Seq: 1, TmpTTL: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(off), `"tmp_ttl_ns":0`) {
		t.Fatalf("reload disabling the TTL lacks tmp_ttl_ns: %s", off)
	}
	var r Reload
	if err := json.Unmarshal([]byte(`{"seq":2,"tmp_ttl_ns":60000000000}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.TmpTTL == nil || *r.TmpTTL != time.Minute {
		t.Fatalf("decoded TmpTTL = %v, want 1m", r.TmpTTL)
	}
	usage, err := json.Marshal(TmpUsage{CapacityBytes: 3, UsedBytes: 2, AvailableBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"capacity_bytes":3,"used_bytes":2,"available_bytes":1}`; string(usage) != want {
		t.Fatalf("usage JSON = %s, want %s", usage, want)
	}
}
