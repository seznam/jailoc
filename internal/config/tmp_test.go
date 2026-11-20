package config

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseTmpSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    int64
		wantErr bool
	}{
		{name: "bytes", in: "4096", want: 4096},
		{name: "kibibytes", in: "64k", want: 64 << 10},
		{name: "mebibytes", in: "512m", want: 512 << 20},
		{name: "gibibytes", in: "1g", want: 1 << 30},
		{name: "uppercase suffix", in: "2G", want: 2 << 30},
		{name: "max int64 bytes", in: "9223372036854775807", want: math.MaxInt64},
		{name: "largest whole gibibytes", in: "8589934591g", want: 8589934591 << 30},
		{name: "empty", in: "", wantErr: true},
		{name: "zero", in: "0", wantErr: true},
		{name: "zero with unit", in: "0g", wantErr: true},
		{name: "negative", in: "-1g", wantErr: true},
		{name: "leading zero", in: "01g", wantErr: true},
		{name: "plus sign", in: "+1g", wantErr: true},
		{name: "decimal", in: "1.5g", wantErr: true},
		{name: "long unit", in: "1gb", wantErr: true},
		{name: "unit only", in: "g", wantErr: true},
		{name: "space", in: "1 g", wantErr: true},
		{name: "terabytes", in: "1t", wantErr: true},
		{name: "bytes overflow", in: "9223372036854775808", wantErr: true},
		{name: "kibibytes overflow", in: "9007199254740992k", wantErr: true},
		{name: "gibibytes overflow", in: "8589934592g", wantErr: true},
		{name: "huge digits", in: "99999999999999999999999999g", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseTmpSize(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseTmpSize(%q) error = %v, wantErr %t", tt.in, err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "tmp_size") {
				t.Errorf("error %q does not name tmp_size", err)
			}
			if got != tt.want {
				t.Errorf("ParseTmpSize(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseTmpTTL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr bool
	}{
		{name: "disabled", in: "0", want: 0},
		{name: "hours", in: "8h", want: 8 * time.Hour},
		{name: "minutes", in: "15m", want: 15 * time.Minute},
		{name: "compound", in: "1h30m", want: 90 * time.Minute},
		{name: "no arbitrary minimum", in: "1ns", want: time.Nanosecond},
		{name: "seconds", in: "1s", want: time.Second},
		{name: "empty", in: "", wantErr: true},
		{name: "zero duration with unit", in: "0s", wantErr: true},
		{name: "negative", in: "-1h", wantErr: true},
		{name: "bare number", in: "5", wantErr: true},
		{name: "garbage", in: "soon", wantErr: true},
		{name: "days", in: "1d", wantErr: true},
		{name: "overflow", in: "9223372036854775808ns", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseTmpTTL(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseTmpTTL(%q) error = %v, wantErr %t", tt.in, err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "tmp_ttl") {
				t.Errorf("error %q does not name tmp_ttl", err)
			}
			if got != tt.want {
				t.Errorf("ParseTmpTTL(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveTmp(t *testing.T) {
	t.Parallel()

	str := func(s string) *string { return &s }
	tests := []struct {
		name     string
		defaults Defaults
		ws       Workspace
		want     Tmp
		errSubst string
	}{
		{
			name: "host defaults",
			want: Tmp{Storage: TmpStorageRAM, SizeBytes: 1 << 30, TTL: 8 * time.Hour},
		},
		{
			name:     "defaults apply",
			defaults: Defaults{TmpStorage: str("disk"), TmpSize: str("2g"), TmpTTL: str("15m")},
			want:     Tmp{Storage: TmpStorageDisk, SizeBytes: 2 << 30, TTL: 15 * time.Minute},
		},
		{
			name:     "workspace overrides defaults",
			defaults: Defaults{TmpStorage: str("disk"), TmpSize: str("2g"), TmpTTL: str("15m")},
			ws:       Workspace{TmpStorage: str("ram"), TmpSize: str("512m"), TmpTTL: str("0")},
			want:     Tmp{Storage: TmpStorageRAM, SizeBytes: 512 << 20, TTL: 0},
		},
		{
			name: "ttl zero disables",
			ws:   Workspace{TmpTTL: str("0")},
			want: Tmp{Storage: TmpStorageRAM, SizeBytes: 1 << 30, TTL: 0},
		},
		{
			name: "ram accepts small sizes",
			ws:   Workspace{TmpSize: str("1")},
			want: Tmp{Storage: TmpStorageRAM, SizeBytes: 1, TTL: 8 * time.Hour},
		},
		{
			name: "disk minimum is accepted",
			ws:   Workspace{TmpStorage: str("disk"), TmpSize: str("16m")},
			want: Tmp{Storage: TmpStorageDisk, SizeBytes: 16 << 20, TTL: 8 * time.Hour},
		},
		{
			name:     "disk below minimum",
			ws:       Workspace{TmpStorage: str("disk"), TmpSize: str("16777215")},
			errSubst: "at least 16 MiB",
		},
		{
			name:     "disk below minimum through defaults size",
			defaults: Defaults{TmpSize: str("8m")},
			ws:       Workspace{TmpStorage: str("disk")},
			errSubst: "at least 16 MiB",
		},
		{
			name:     "disk below minimum through defaults storage",
			defaults: Defaults{TmpStorage: str("disk")},
			ws:       Workspace{TmpSize: str("1m")},
			errSubst: "at least 16 MiB",
		},
		{
			name:     "empty storage",
			ws:       Workspace{TmpStorage: str("")},
			errSubst: "tmp_storage",
		},
		{
			name:     "unknown storage",
			ws:       Workspace{TmpStorage: str("tmpfs")},
			errSubst: "tmp_storage",
		},
		{
			name:     "uppercase storage",
			ws:       Workspace{TmpStorage: str("RAM")},
			errSubst: "tmp_storage",
		},
		{
			name:     "bad size",
			ws:       Workspace{TmpSize: str("0")},
			errSubst: "tmp_size",
		},
		{
			name:     "bad ttl",
			ws:       Workspace{TmpTTL: str("-1h")},
			errSubst: "tmp_ttl",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveTmp(tt.defaults, tt.ws)
			if tt.errSubst != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errSubst) {
					t.Fatalf("ResolveTmp error = %v, want substring %q", err, tt.errSubst)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveTmp: %v", err)
			}
			if got != tt.want {
				t.Errorf("ResolveTmp = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadParsesTmpSettings(t *testing.T) {
	t.Parallel()

	path := t.TempDir() + "/config.toml"
	writeFile(t, path, `
[defaults]
tmp_storage = "disk"
tmp_size = "2g"
tmp_ttl = "15m"

[workspaces.a]
paths = ["/data"]
tmp_storage = "ram"
tmp_size = "256m"
tmp_ttl = "0"

[workspaces.b]
paths = ["/data"]
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	d := cfg.Defaults
	if d.TmpStorage == nil || *d.TmpStorage != "disk" || d.TmpSize == nil || *d.TmpSize != "2g" || d.TmpTTL == nil || *d.TmpTTL != "15m" {
		t.Errorf("defaults tmp settings = %v %v %v", d.TmpStorage, d.TmpSize, d.TmpTTL)
	}
	a := cfg.Workspaces["a"]
	if a.TmpStorage == nil || *a.TmpStorage != "ram" || a.TmpSize == nil || *a.TmpSize != "256m" || a.TmpTTL == nil || *a.TmpTTL != "0" {
		t.Errorf("workspace a tmp settings = %v %v %v", a.TmpStorage, a.TmpSize, a.TmpTTL)
	}
	b := cfg.Workspaces["b"]
	if b.TmpStorage != nil || b.TmpSize != nil || b.TmpTTL != nil {
		t.Errorf("workspace b tmp settings = %v %v %v, want unset", b.TmpStorage, b.TmpSize, b.TmpTTL)
	}
}

func TestValidateTmpSettings(t *testing.T) {
	t.Parallel()

	str := func(s string) *string { return &s }
	tests := []struct {
		name     string
		cfg      *Config
		errSubst []string
	}{
		{
			name:     "defaults invalid storage",
			cfg:      &Config{Defaults: Defaults{TmpStorage: str("")}, Workspaces: map[string]Workspace{}},
			errSubst: []string{"defaults", "tmp_storage"},
		},
		{
			name:     "defaults invalid size",
			cfg:      &Config{Defaults: Defaults{TmpSize: str("1gb")}, Workspaces: map[string]Workspace{}},
			errSubst: []string{"defaults", "tmp_size"},
		},
		{
			name:     "defaults invalid ttl",
			cfg:      &Config{Defaults: Defaults{TmpTTL: str("0s")}, Workspaces: map[string]Workspace{}},
			errSubst: []string{"defaults", "tmp_ttl"},
		},
		{
			name:     "defaults disk too small",
			cfg:      &Config{Defaults: Defaults{TmpStorage: str("disk"), TmpSize: str("1m")}, Workspaces: map[string]Workspace{}},
			errSubst: []string{"defaults", "16 MiB"},
		},
		{
			name: "workspace invalid storage",
			cfg: &Config{Workspaces: map[string]Workspace{
				"test": {Paths: []string{"/data"}, TmpStorage: str("tmpfs")},
			}},
			errSubst: []string{`workspace "test"`, "tmp_storage"},
		},
		{
			name: "workspace invalid size",
			cfg: &Config{Workspaces: map[string]Workspace{
				"test": {Paths: []string{"/data"}, TmpSize: str("-1")},
			}},
			errSubst: []string{`workspace "test"`, "tmp_size"},
		},
		{
			name: "workspace invalid ttl",
			cfg: &Config{Workspaces: map[string]Workspace{
				"test": {Paths: []string{"/data"}, TmpTTL: str("soon")},
			}},
			errSubst: []string{`workspace "test"`, "tmp_ttl"},
		},
		{
			name: "workspace disk too small through defaults size",
			cfg: &Config{
				Defaults: Defaults{TmpSize: str("4m")},
				Workspaces: map[string]Workspace{
					"test": {Paths: []string{"/data"}, TmpStorage: str("disk")},
				},
			},
			errSubst: []string{`workspace "test"`, "16 MiB"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCopy(tt.cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			for _, sub := range tt.errSubst {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q missing %q", err, sub)
				}
			}
		})
	}

	valid := &Config{
		Defaults: Defaults{TmpStorage: str("disk"), TmpSize: str("64m"), TmpTTL: str("0")},
		Workspaces: map[string]Workspace{
			"test": {Paths: []string{"/data"}, TmpStorage: str("ram"), TmpSize: str("1m")},
		},
	}
	if err := ValidateCopy(valid); err != nil {
		t.Errorf("valid tmp settings rejected: %v", err)
	}
}
