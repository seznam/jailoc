package docker

import (
	"bytes"
	"context"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/embed"
	"github.com/seznam/jailoc/internal/guest"
)

func TestCAFrontend(t *testing.T) {
	t.Parallel()

	digest := "@sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		name       string
		dockerfile string
		wantErr    bool
	}{
		{"no syntax", "FROM ubuntu\n", false},
		{"empty", "", false},
		{"tag 1", "# syntax=docker/dockerfile:1\nFROM ubuntu\n", false},
		{"tag 1-labs", "# syntax=docker/dockerfile:1-labs\nFROM ubuntu\n", false},
		{"tag latest", "# syntax=docker/dockerfile:latest\nFROM ubuntu\n", false},
		{"tag 1.3", "# syntax=docker/dockerfile:1.3\nFROM ubuntu\n", false},
		{"tag 1.7.1", "# syntax=docker/dockerfile:1.7.1\nFROM ubuntu\n", false},
		{"tag 1.10-labs", "# syntax=docker/dockerfile:1.10-labs\nFROM ubuntu\n", false},
		{"tag 1.4.0-labs", "# syntax=docker/dockerfile:1.4.0-labs\nFROM ubuntu\n", false},
		{"docker.io prefix", "# syntax=docker.io/docker/dockerfile:1\nFROM ubuntu\n", false},
		{"digest", "# syntax=docker/dockerfile@" + digest[1:] + "\nFROM ubuntu\n", false},
		{"tag and digest", "# syntax=docker/dockerfile:1" + digest + "\nFROM ubuntu\n", false},
		{"docker.io digest", "# syntax=docker.io/docker/dockerfile" + digest + "\nFROM ubuntu\n", false},
		{"tag 1.0", "# syntax=docker/dockerfile:1.0\nFROM ubuntu\n", true},
		{"tag 1.1", "# syntax=docker/dockerfile:1.1\nFROM ubuntu\n", true},
		{"tag 1.1-labs", "# syntax=docker/dockerfile:1.1-labs\nFROM ubuntu\n", true},
		{"tag 1.2", "# syntax=docker/dockerfile:1.2\nFROM ubuntu\n", true},
		{"tag 2", "# syntax=docker/dockerfile:2\nFROM ubuntu\n", true},
		{"tag 1.x", "# syntax=docker/dockerfile:1.x\nFROM ubuntu\n", true},
		{"no tag", "# syntax=docker/dockerfile\nFROM ubuntu\n", true},
		{"upstream", "# syntax=docker/dockerfile-upstream:1\nFROM ubuntu\n", true},
		{"custom registry", "# syntax=example.com/custom:1\nFROM ubuntu\n", true},
		{"foreign registry", "# syntax=example.com/docker/dockerfile:1\nFROM ubuntu\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkCAFrontend([]byte(tt.dockerfile))
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkCAFrontend() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				for _, want := range []string{"dockerfile syntax", "ca_bundle", "docker/dockerfile frontend (1.3 or later)", "ca_bundle = false"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q lacks %q", err, want)
					}
				}
			}
		})
	}
}

func TestCAPlanTrust(t *testing.T) {
	t.Parallel()

	arch := runtime.GOARCH
	bp := "FROM --platform=$BUILDPLATFORM"
	str := func(s string) *string { return &s }

	tests := []struct {
		name       string
		dockerfile string
		args       map[string]*string
		wantStages []caStage
		wantImages []string
	}{
		{
			name:       "single stage",
			dockerfile: "FROM ubuntu:24.04\nRUN true\n",
			wantStages: []caStage{{fromEndLine: 1, base: "ubuntu:24.04", name: "0", argKeys: map[string]bool{}}},
			wantImages: []string{"ubuntu:24.04"},
		},
		{
			name: "multistage skips stage alias",
			dockerfile: "FROM golang:1.26 AS build\nARG VERSION\nRUN go build\n" +
				"FROM build AS test\nRUN go test\n",
			wantStages: []caStage{{fromEndLine: 1, base: "golang:1.26", name: "build", argKeys: map[string]bool{}}},
			wantImages: []string{"golang:1.26"},
		},
		{
			name:       "stage alias is case-insensitive",
			dockerfile: "FROM alpine:3 AS Build\nFROM BUILD\n",
			wantStages: []caStage{{fromEndLine: 1, base: "alpine:3", name: "build", argKeys: map[string]bool{}}},
			wantImages: []string{"alpine:3"},
		},
		{
			name:       "FROM expands meta ARG default",
			dockerfile: "ARG BASE=debian:12\nFROM ${BASE}\n",
			wantStages: []caStage{{fromEndLine: 2, base: "debian:12", name: "0", argKeys: map[string]bool{}}},
			wantImages: []string{"debian:12"},
		},
		{
			name:       "build arg overrides meta ARG default",
			dockerfile: "ARG BASE=debian:12\nFROM ${BASE}\n",
			args:       map[string]*string{"BASE": str("ubuntu:26.04")},
			wantStages: []caStage{{fromEndLine: 2, base: "ubuntu:26.04", name: "0", argKeys: map[string]bool{}}},
			wantImages: []string{"ubuntu:26.04"},
		},
		{
			name:       "meta ARG without default takes build arg",
			dockerfile: "ARG BASE\nFROM ${BASE}\n",
			args:       map[string]*string{"BASE": str("ubuntu:26.04")},
			wantStages: []caStage{{fromEndLine: 2, base: "ubuntu:26.04", name: "0", argKeys: map[string]bool{}}},
			wantImages: []string{"ubuntu:26.04"},
		},
		{
			name:       "meta ARG default references earlier ARG",
			dockerfile: "ARG V=24.04\nARG BASE=ubuntu:${V}\nFROM ${BASE}\n",
			wantStages: []caStage{{fromEndLine: 3, base: "ubuntu:24.04", name: "0", argKeys: map[string]bool{}}},
			wantImages: []string{"ubuntu:24.04"},
		},
		{
			name:       "platform from BUILDPLATFORM",
			dockerfile: bp + " alpine:3 AS a\n",
			wantStages: []caStage{{fromEndLine: 1, base: "alpine:3", name: "a", argKeys: map[string]bool{}}},
			wantImages: []string{"alpine:3"},
		},
		{
			name:       "platform linux",
			dockerfile: "FROM --platform=linux alpine:3\n",
			wantStages: []caStage{{fromEndLine: 1, base: "alpine:3", name: "0", argKeys: map[string]bool{}}},
			wantImages: []string{"alpine:3"},
		},
		{
			name:       "platform linux/arch",
			dockerfile: "FROM --platform=linux/" + arch + " alpine:3\n",
			wantStages: []caStage{{fromEndLine: 1, base: "alpine:3", name: "0", argKeys: map[string]bool{}}},
			wantImages: []string{"alpine:3"},
		},
		{
			name:       "platform from TARGETARCH",
			dockerfile: "FROM --platform=linux/$TARGETARCH alpine:3\n",
			wantStages: []caStage{{fromEndLine: 1, base: "alpine:3", name: "0", argKeys: map[string]bool{}}},
			wantImages: []string{"alpine:3"},
		},
		{
			name:       "continued FROM reports its last line",
			dockerfile: "# comment\nFROM \\\n  ubuntu:24.04 \\\n  AS base\nRUN true\n",
			wantStages: []caStage{{fromEndLine: 4, base: "ubuntu:24.04", name: "base", argKeys: map[string]bool{}}},
			wantImages: []string{"ubuntu:24.04"},
		},
		{
			name: "COPY --from numeric index and stage name are not images",
			dockerfile: "FROM alpine:3 AS a\nFROM debian:12\n" +
				"COPY --from=0 /a /a\nCOPY --from=a /b /b\nCOPY --from=A /c /c\n",
			wantStages: []caStage{
				{fromEndLine: 1, base: "alpine:3", name: "a", argKeys: map[string]bool{}},
				{fromEndLine: 2, base: "debian:12", name: "1", argKeys: map[string]bool{}},
			},
			wantImages: []string{"alpine:3", "debian:12"},
		},
		{
			name:       "COPY --from external image",
			dockerfile: "FROM alpine:3\nCOPY --from=busybox:1 /bin/busybox /bin/busybox\nCOPY --from=busybox:1 /bin/sh /bin/sh\n",
			wantStages: []caStage{{fromEndLine: 1, base: "alpine:3", name: "0", argKeys: map[string]bool{}}},
			wantImages: []string{"alpine:3", "busybox:1"},
		},
		{
			name:       "COPY --from expands stage ARG",
			dockerfile: "FROM alpine:3\nARG TOOL=busybox:1\nCOPY --from=${TOOL} /bin/busybox /bin/busybox\n",
			wantStages: []caStage{{fromEndLine: 1, base: "alpine:3", name: "0", argKeys: map[string]bool{"TOOL": true}}},
			wantImages: []string{"alpine:3", "busybox:1"},
		},
		{
			name:       "RUN mount from external image",
			dockerfile: "FROM alpine:3\nRUN --mount=type=bind,from=ext:1,target=/ext true\nRUN --mount=type=cache,target=/c true\nRUN --mount=type=bind,target=/ctx true\n",
			wantStages: []caStage{{fromEndLine: 1, base: "alpine:3", name: "0", argKeys: map[string]bool{}}},
			wantImages: []string{"alpine:3", "ext:1"},
		},
		{
			name:       "RUN mount from stage is not an image",
			dockerfile: "FROM alpine:3 AS tools\nFROM debian:12\nRUN --mount=type=bind,from=tools,target=/t true\nRUN --mount=type=bind,from=0,target=/z true\n",
			wantStages: []caStage{
				{fromEndLine: 1, base: "alpine:3", name: "tools", argKeys: map[string]bool{}},
				{fromEndLine: 2, base: "debian:12", name: "1", argKeys: map[string]bool{}},
			},
			wantImages: []string{"alpine:3", "debian:12"},
		},
		{
			name: "COPY --from and RUN mount from a later stage are not images",
			dockerfile: "FROM alpine:3\nCOPY --from=tools /bin/tool /usr/local/bin/tool\n" +
				"RUN --mount=type=bind,from=tools,source=/src,target=/mnt true\n" +
				"FROM debian:bookworm AS tools\n",
			wantStages: []caStage{
				{fromEndLine: 1, base: "alpine:3", name: "0", argKeys: map[string]bool{}},
				{fromEndLine: 4, base: "debian:bookworm", name: "tools", argKeys: map[string]bool{}},
			},
			wantImages: []string{"alpine:3", "debian:bookworm"},
		},
		{
			name: "FROM a later stage alias is an external image",
			dockerfile: "FROM tools\n" +
				"FROM debian:bookworm AS tools\n",
			wantStages: []caStage{
				{fromEndLine: 1, base: "tools", name: "0", argKeys: map[string]bool{}},
				{fromEndLine: 2, base: "debian:bookworm", name: "tools", argKeys: map[string]bool{}},
			},
			wantImages: []string{"tools", "debian:bookworm"},
		},
		{
			name:       "images deduplicated in order of appearance",
			dockerfile: "FROM b:1 AS x\nCOPY --from=a:1 /a /a\nFROM a:1\nCOPY --from=b:1 /b /b\n",
			wantStages: []caStage{
				{fromEndLine: 1, base: "b:1", name: "x", argKeys: map[string]bool{}},
				{fromEndLine: 3, base: "a:1", name: "1", argKeys: map[string]bool{}},
			},
			wantImages: []string{"b:1", "a:1"},
		},
		{
			name:       "RUN mount options from the base image ENV are not expanded",
			dockerfile: "FROM alpine:3\nRUN --mount=type=cache,target=/cache,uid=${CACHE_UID},gid=${CACHE_GID},mode=${CACHE_MODE},size=${CACHE_SIZE},readonly=${CACHE_RO} true\n",
			wantStages: []caStage{{fromEndLine: 1, base: "alpine:3", name: "0", argKeys: map[string]bool{}}},
			wantImages: []string{"alpine:3"},
		},
		{
			name:       "RUN mount from is read while other options keep variables",
			dockerfile: "FROM alpine:3\nRUN --mount=type=bind,from=ext:1,target=/mnt,uid=${CACHE_UID} true\n",
			wantStages: []caStage{{fromEndLine: 1, base: "alpine:3", name: "0", argKeys: map[string]bool{}}},
			wantImages: []string{"alpine:3", "ext:1"},
		},
		{
			name:       "RUN mount from in quoted value",
			dockerfile: "FROM alpine:3\nRUN --mount=type=bind,target=/mnt,\"from=ext:2\" true\n",
			wantStages: []caStage{{fromEndLine: 1, base: "alpine:3", name: "0", argKeys: map[string]bool{}}},
			wantImages: []string{"alpine:3", "ext:2"},
		},
		{
			name:       "RUN mounts on a continued instruction and in later stages",
			dockerfile: "FROM alpine:3 AS a\nFROM debian:12\nRUN --mount=type=cache,target=/c,uid=${U} \\\n    --mount=type=bind,from=ext:3,target=/e \\\n    true\n",
			wantStages: []caStage{
				{fromEndLine: 1, base: "alpine:3", name: "a", argKeys: map[string]bool{}},
				{fromEndLine: 2, base: "debian:12", name: "1", argKeys: map[string]bool{}},
			},
			wantImages: []string{"alpine:3", "debian:12", "ext:3"},
		},
		{
			name:       "only valued ARG keys are recorded as overrides",
			dockerfile: "ARG M=meta\nARG UNSET_META\nFROM alpine:3\nARG A\nRUN true\nARG B=1 C D M UNSET_META\n",
			args:       map[string]*string{"D": str("from-build-arg")},
			wantStages: []caStage{{fromEndLine: 3, base: "alpine:3", name: "0", argKeys: map[string]bool{"B": true, "D": true, "M": true}}},
			wantImages: []string{"alpine:3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			plan, err := planCATrust([]byte(tt.dockerfile), tt.args)
			if err != nil {
				t.Fatalf("planCATrust() error = %v", err)
			}
			if !reflect.DeepEqual(plan.stages, tt.wantStages) {
				t.Errorf("stages =\n%+v\nwant\n%+v", plan.stages, tt.wantStages)
			}
			if !reflect.DeepEqual(plan.images, tt.wantImages) {
				t.Errorf("images = %v, want %v", plan.images, tt.wantImages)
			}
		})
	}
}

func TestCAPlanTrustErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		dockerfile string
		want       []string
	}{
		{"FROM without value", "FROM ${BASE}\n", []string{"FROM", "ca_bundle"}},
		{"FROM undeclared ARG", "FROM ${BASE}\nRUN true\n", []string{"FROM", "ca_bundle"}},
		{"FROM unresolved with suffix", "FROM ${BASE}:1\n", []string{"FROM", "ca_bundle"}},
		{"foreign os", "FROM --platform=windows/amd64 alpine:3\n", []string{"--platform", "ca_bundle"}},
		{"foreign arch", "FROM --platform=linux/riscv64x alpine:3\n", []string{"--platform", "ca_bundle"}},
		{"foreign platform via ARG", "ARG P=linux/s390x\nFROM --platform=$P alpine:3\n", []string{"--platform", "ca_bundle"}},
		{"syntax error", "RUN true\n", []string{"dockerfile"}},
		// BuildKit's own parser rejects variables in from=.
		{"RUN mount from with variable", "FROM alpine:3\nARG TOOL_IMAGE=ext:1\nRUN --mount=type=bind,from=${TOOL_IMAGE},target=/mnt,uid=${CACHE_UID} true\n", []string{"from", "doesn't support variable expansion"}},
		{"RUN mount from unset variable", "FROM alpine:3\nRUN --mount=type=bind,from=${UNSET_IMAGE},target=/mnt true\n", []string{"from", "doesn't support variable expansion"}},
		{"RUN mount from unresolved", "FROM alpine:3\nRUN --mount=type=bind,from=ext$,target=/mnt true\n", []string{"cannot resolve RUN --mount from=", "ca_bundle"}},
		{"COPY --from unresolved", "FROM alpine:3\nCOPY --from=${X}:1 /a /a\n", []string{"ca_bundle"}},
		{"only scratch", "FROM scratch\nCOPY a /a\n", []string{`stage "0"`, "FROM scratch", "ca_bundle = false"}},
		{"scratch final stage", "FROM golang:1.26 AS build\nFROM scratch\nCOPY --from=build /app /app\n", []string{`stage "1"`, "FROM scratch", "Debian/Ubuntu"}},
		{"scratch in any case via ARG", "ARG B=Scratch\nFROM ${B} AS empty\n", []string{`stage "empty"`, "FROM scratch", "no shell or system trust store"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := planCATrust([]byte(tt.dockerfile), nil)
			if err == nil {
				t.Fatal("planCATrust() error = nil, want error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

func TestCATrustInstructions(t *testing.T) {
	t.Parallel()

	const digest = "0123456789abcdef"
	envLine := func(k, v string) string { return "ENV " + k + "=" + v + "\n" }
	allEnv := func(skip ...string) string {
		var b strings.Builder
		keys := append([]string{"UV_SYSTEM_CERTS", "NODE_USE_SYSTEM_CA"}, guest.CABundleVars...)
		for _, k := range keys {
			if containsString(skip, k) {
				continue
			}
			switch k {
			case "UV_SYSTEM_CERTS":
				b.WriteString(envLine(k, "true"))
			case "NODE_USE_SYSTEM_CA":
				b.WriteString(envLine(k, "1"))
			default:
				b.WriteString(envLine(k, guest.SystemCABundle))
			}
		}
		return b.String()
	}

	t.Run("user switching", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			user         string
			wantSwitch   bool
			wantRestored string
		}{
			{"agent", true, "USER agent\n"},
			{"1000:1000", true, "USER 1000:1000\n"},
			{"", false, ""},
			{"root", false, ""},
			{"0", false, ""},
			{"0:0", false, ""},
		}
		for _, tt := range tests {
			t.Run("user="+tt.user, func(t *testing.T) {
				t.Parallel()
				got := caTrustInstructions(digest, tt.user, nil, nil)
				if tt.wantSwitch {
					if !strings.HasPrefix(got, "USER root\nRUN ") {
						t.Errorf("want USER root before RUN:\n%s", got)
					}
					if !strings.HasSuffix(got, allEnv()+tt.wantRestored) {
						t.Errorf("want ENV lines then %q at the end:\n%s", tt.wantRestored, got)
					}
					return
				}
				if strings.Contains(got, "USER ") {
					t.Errorf("unexpected USER instruction:\n%s", got)
				}
				if !strings.HasPrefix(got, "RUN ") || !strings.HasSuffix(got, allEnv()) {
					t.Errorf("want RUN then ENV lines:\n%s", got)
				}
			})
		}
	})

	t.Run("env skips base env and ARG keys", func(t *testing.T) {
		t.Parallel()
		baseEnv := []string{"PATH=/usr/bin", "SSL_CERT_FILE=/custom.pem", "UV_SYSTEM_CERTS=false", "NODE_EXTRA_CA_CERTS="}
		argKeys := map[string]bool{"PIP_CERT": true, "NODE_USE_SYSTEM_CA": true}
		got := caTrustInstructions(digest, "", baseEnv, argKeys)
		want := allEnv("SSL_CERT_FILE", "UV_SYSTEM_CERTS", "NODE_EXTRA_CA_CERTS", "PIP_CERT", "NODE_USE_SYSTEM_CA")
		if !strings.HasSuffix(got, want) {
			t.Errorf("ENV lines =\n%s\nwant suffix\n%s", got, want)
		}
		for _, k := range []string{"SSL_CERT_FILE", "UV_SYSTEM_CERTS", "NODE_EXTRA_CA_CERTS", "PIP_CERT", "NODE_USE_SYSTEM_CA"} {
			if strings.Contains(got, "ENV "+k+"=") {
				t.Errorf("ENV %s should be skipped:\n%s", k, got)
			}
		}
	})

	t.Run("RUN instruction", func(t *testing.T) {
		t.Parallel()
		got := caTrustInstructions(digest, "agent", nil, nil)
		var run string
		for line := range strings.SplitSeq(got, "\n") {
			if strings.HasPrefix(line, "RUN ") {
				run = line
			}
		}
		if run == "" {
			t.Fatalf("no RUN instruction:\n%s", got)
		}
		if strings.Count(got, "RUN ") != 1 {
			t.Errorf("want exactly one RUN:\n%s", got)
		}
		prefix := `RUN --network=none --mount=type=secret,id=jailoc-ca,required=true ["/bin/sh", "-c", ": ` + caTrustMarker + ` sha256:` + digest + `; set -eu; `
		if !strings.HasPrefix(run, prefix) {
			t.Errorf("RUN = %s\nwant prefix %s", run, prefix)
		}
		if strings.Contains(run, "$") {
			t.Errorf("RUN must not contain $ (BuildKit expands it): %s", run)
		}
		for _, want := range []string{
			"/etc/debian_version",
			"supports Debian/Ubuntu build stages only",
			"/usr/local/share/ca-certificates/jailoc-build-ca-",
			"/run/secrets/jailoc-ca",
			"update-ca-certificates",
			"/etc/ssl/certs/ca-certificates.crt",
			"chmod 0644",
			"/tmp/jailoc-old-ca.crt",
			"/tmp/jailoc-prev-ca.crt",
			"else cat /usr/local/share/ca-certificates/jailoc-build-ca-*.crt > /etc/ssl/certs/ca-certificates.crt; fi && if [ -s /tmp/jailoc-prev-ca.crt ]; then",
		} {
			if !strings.Contains(run, want) {
				t.Errorf("RUN lacks %q: %s", want, run)
			}
		}
	})

	t.Run("RUN parses as exec form with the secret mount", func(t *testing.T) {
		t.Parallel()
		stages := parseStages(t, []byte("FROM ubuntu:26.04\n"+caTrustInstructions(digest, "agent", []string{"A=b"}, nil)))
		if len(stages) != 1 {
			t.Fatalf("got %d stages, want 1", len(stages))
		}
		var runs []*instructions.RunCommand
		for _, c := range stages[0].Commands {
			if r, ok := c.(*instructions.RunCommand); ok {
				runs = append(runs, r)
			}
		}
		if len(runs) != 1 {
			t.Fatalf("got %d RUN commands, want 1", len(runs))
		}
		r := runs[0]
		if r.PrependShell || len(r.CmdLine) != 3 || r.CmdLine[0] != "/bin/sh" || r.CmdLine[1] != "-c" {
			t.Errorf("RUN is not exec-form /bin/sh -c: %+v", r.ShellDependantCmdLine)
		}
		if !strings.HasPrefix(r.CmdLine[2], ": "+caTrustMarker+" sha256:"+digest+"; set -eu; ") {
			t.Errorf("script = %q", r.CmdLine[2])
		}
		// The parser leaves mounts unparsed until they are expanded.
		if err := r.Expand(func(word string) (string, error) { return word, nil }); err != nil {
			t.Fatalf("expand RUN: %v", err)
		}
		mounts := instructions.GetMounts(r)
		if len(mounts) != 1 || mounts[0].Type != instructions.MountTypeSecret || mounts[0].CacheID != caSecretID || !mounts[0].Required {
			t.Errorf("mounts = %+v, want one required secret %q", *mounts[0], caSecretID)
		}
	})

	t.Run("digest changes the RUN", func(t *testing.T) {
		t.Parallel()
		a := caTrustInstructions("aaaa", "", nil, nil)
		b := caTrustInstructions("bbbb", "", nil, nil)
		if a == b {
			t.Error("different digests must produce different instructions")
		}
		if !strings.Contains(a, "sha256:aaaa") || !strings.Contains(b, "sha256:bbbb") {
			t.Error("instructions must carry the literal digest")
		}
	})
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func parseStages(t *testing.T, dockerfile []byte) []instructions.Stage {
	t.Helper()
	res, err := parser.Parse(bytes.NewReader(dockerfile))
	if err != nil {
		t.Fatalf("parse Dockerfile: %v\n%s", err, dockerfile)
	}
	stages, _, err := instructions.Parse(res.AST, nil)
	if err != nil {
		t.Fatalf("parse instructions: %v\n%s", err, dockerfile)
	}
	return stages
}

func TestCAInjectAfterLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		inserts map[int]string
		want    string
	}{
		{"no inserts", "FROM a\nRUN b\n", nil, "FROM a\nRUN b\n"},
		{"after first line", "FROM a\nRUN b\n", map[int]string{1: "X\n"}, "FROM a\nX\nRUN b\n"},
		{"after last line", "FROM a\nRUN b\n", map[int]string{2: "X\n"}, "FROM a\nRUN b\nX\n"},
		{"multiple", "FROM a\nRUN b\nFROM c\n", map[int]string{1: "X\n", 3: "Y\n"}, "FROM a\nX\nRUN b\nFROM c\nY\n"},
		{"no trailing newline", "FROM a", map[int]string{1: "X\n"}, "FROM a\nX\n"},
		{"crlf preserved", "FROM a\r\nRUN b\r\n", map[int]string{1: "X\n"}, "FROM a\r\nX\nRUN b\r\n"},
		{"insert without trailing newline gets one", "FROM a\nRUN b\n", map[int]string{1: "X"}, "FROM a\nX\nRUN b\n"},
		{"out of range line is ignored", "FROM a\n", map[int]string{0: "X\n", 9: "Y\n"}, "FROM a\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orig := tt.in
			got := string(injectAfterLines([]byte(tt.in), tt.inserts))
			if got != tt.want {
				t.Errorf("injectAfterLines() = %q, want %q", got, tt.want)
			}
			if tt.in != orig {
				t.Error("input was mutated")
			}
		})
	}
}

// injectTrust plans dockerfile and inserts the trust instructions after every
// planned stage. It returns the result, the plan and the inserted blocks.
func injectTrust(t *testing.T, dockerfile []byte, args map[string]*string, user string) ([]byte, caPlan, map[int]string) {
	t.Helper()
	plan, err := planCATrust(dockerfile, args)
	if err != nil {
		t.Fatalf("planCATrust: %v", err)
	}
	inserts := make(map[int]string, len(plan.stages))
	for _, st := range plan.stages {
		inserts[st.fromEndLine] = caTrustInstructions("abcd", user, nil, st.argKeys)
	}
	return injectAfterLines(dockerfile, inserts), plan, inserts
}

func TestCAInjectKeepsDockerfileConstructs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		dockerfile string
		stages     int
	}{
		{
			name:       "heredoc RUN",
			dockerfile: "FROM ubuntu:26.04\nRUN <<EOF\nFROM fake\necho hi\nEOF\nRUN true\n",
			stages:     1,
		},
		{
			name:       "heredoc between stages",
			dockerfile: "FROM ubuntu:26.04 AS a\nCOPY <<EOF /x\nFROM nope\nEOF\nFROM debian:12\nRUN true\n",
			stages:     2,
		},
		{
			name:       "exec-form RUN and SHELL",
			dockerfile: "FROM ubuntu:26.04\nSHELL [\"/bin/bash\", \"-c\"]\nRUN [\"echo\", \"hi\"]\nCMD [\"x\"]\n",
			stages:     1,
		},
		{
			name:       "escape directive",
			dockerfile: "# escape=`\nFROM ubuntu:26.04 `\n  AS base\nRUN echo a `\n  b\n",
			stages:     1,
		},
		{
			name:       "syntax directive and comments",
			dockerfile: "# syntax=docker/dockerfile:1\n# a comment\nFROM ubuntu:26.04\n# trailing\nRUN true\n",
			stages:     1,
		},
		{
			name:       "CRLF",
			dockerfile: "FROM ubuntu:26.04\r\nRUN echo a \\\r\n  b\r\nFROM debian:12\r\n",
			stages:     2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orig := []byte(tt.dockerfile)
			got, plan, inserts := injectTrust(t, orig, nil, "agent")
			if len(plan.stages) != tt.stages {
				t.Fatalf("planned %d stages, want %d", len(plan.stages), tt.stages)
			}
			assertInjectedWellFormed(t, orig, got, plan, inserts)
		})
	}
}

func TestCAInjectEmbeddedDockerfiles(t *testing.T) {
	t.Parallel()

	claude, ok := agent.Lookup("claude")
	if !ok {
		t.Fatal("claude harness not found")
	}
	base := "ubuntu:26.04"
	baseArgs := map[string]*string{"BASE": &base}
	tests := []struct {
		name       string
		dockerfile []byte
		args       map[string]*string
		user       string
	}{
		{"embedded base", embed.Dockerfile(), nil, ""},
		{"docker layer", dockerLayerDockerfile(embed.DockerLayer(), "agent"), baseArgs, "agent"},
		{"agent layer", agentDockerfile(claude, "agent"), baseArgs, "agent"},
		{"agent layer root", agentDockerfile(claude, "root"), baseArgs, "root"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, plan, inserts := injectTrust(t, tt.dockerfile, tt.args, tt.user)
			if len(plan.stages) == 0 {
				t.Fatal("planned no stages")
			}
			for _, st := range plan.stages {
				if st.base != base {
					t.Errorf("stage %q base = %q, want %q", st.name, st.base, base)
				}
			}
			assertInjectedWellFormed(t, tt.dockerfile, got, plan, inserts)
		})
	}
}

// assertInjectedWellFormed checks that removing the inserted blocks from got
// gives orig back byte for byte, that every block directly follows a FROM,
// and that the result still parses with one trust RUN per planned stage.
func assertInjectedWellFormed(t *testing.T, orig, got []byte, plan caPlan, inserts map[int]string) {
	t.Helper()

	restored := string(got)
	for line, block := range inserts {
		if !strings.Contains(restored, block) {
			t.Fatalf("block for line %d missing:\n%s", line, got)
		}
		restored = strings.Replace(restored, block, "", 1)
	}
	if restored != string(orig) {
		t.Errorf("removing the inserted blocks gives\n%q\nwant the original\n%q", restored, orig)
	}

	stages := parseStages(t, got)
	runs := 0
	for _, st := range stages {
		for i, c := range st.Commands {
			r, ok := c.(*instructions.RunCommand)
			if !ok || len(r.CmdLine) != 3 || !strings.HasPrefix(r.CmdLine[2], ": "+caTrustMarker+" ") {
				continue
			}
			runs++
			// Only USER root may sit between the FROM and the trust RUN.
			if i > 1 {
				t.Errorf("trust RUN is command %d of stage %q, want first (after USER root)", i, st.Name)
			}
		}
	}
	if runs != len(plan.stages) {
		t.Errorf("found %d trust RUN instructions, want %d", runs, len(plan.stages))
	}
}

func TestPrepareCADockerfileNilCA(t *testing.T) {
	t.Parallel()
	// A nil client and an unparsable Dockerfile prove nothing is touched.
	in := []byte("not a dockerfile\n")
	got, err := prepareCADockerfile(context.Background(), nil, in, nil, nil)
	if err != nil || !bytes.Equal(got, in) {
		t.Fatalf("prepareCADockerfile = %q, %v, want the input unchanged", got, err)
	}
}

func TestPrepareCADockerfileInjectsTrust(t *testing.T) {
	t.Parallel()
	ca := testBuildCA(t)
	agentEnv := []string{"PATH=/usr/bin", "SSL_CERT_FILE=/custom/ca.pem"}
	e := &fakeEngine{images: map[string]fakeImage{
		"ubuntu:26.04": {},
		"golang:1.26":  {user: "agent", env: agentEnv},
	}}
	dockerfile := []byte("FROM golang:1.26 AS build\nARG VERSION\nARG PIP_CERT=/custom/pip.pem\nRUN go build\nFROM ubuntu:26.04\nCOPY --from=build /app /app\n")

	got, err := prepareCADockerfile(context.Background(), e.client(t), dockerfile, nil, ca)
	if err != nil {
		t.Fatalf("prepareCADockerfile: %v", err)
	}
	want := injectAfterLines(dockerfile, map[int]string{
		1: caTrustInstructions(ca.Digest(), "agent", agentEnv, map[string]bool{"PIP_CERT": true}),
		5: caTrustInstructions(ca.Digest(), "", nil, map[string]bool{}),
	})
	if !bytes.Equal(got, want) {
		t.Fatalf("Dockerfile =\n%s\nwant\n%s", got, want)
	}
	text := string(got)
	for _, wantIn := range []string{"USER root\n", "USER agent\n", "sha256:" + ca.Digest()} {
		if !strings.Contains(text, wantIn) {
			t.Errorf("Dockerfile lacks %q:\n%s", wantIn, text)
		}
	}
	// The golang stage's own SSL_CERT_FILE is not overridden; ubuntu's has none.
	if n := strings.Count(text, "ENV SSL_CERT_FILE="); n != 1 {
		t.Errorf("Dockerfile sets SSL_CERT_FILE %d times, want once (ubuntu stage only):\n%s", n, text)
	}
	if strings.Contains(text, string(ca.PEM())) {
		t.Error("Dockerfile contains the CA bundle")
	}
}

func TestPrepareCADockerfileUsesBuildArgs(t *testing.T) {
	t.Parallel()
	ca := testBuildCA(t)
	e := &fakeEngine{images: map[string]fakeImage{"registry.example.com/base:v1": {user: "1000:1000"}}}
	base := "registry.example.com/base:v1"
	got, err := prepareCADockerfile(context.Background(), e.client(t), []byte("ARG BASE\nFROM ${BASE}\nRUN true\n"), map[string]*string{"BASE": &base}, ca)
	if err != nil {
		t.Fatalf("prepareCADockerfile: %v", err)
	}
	if !strings.Contains(string(got), "USER 1000:1000\n") {
		t.Errorf("Dockerfile does not restore the base user:\n%s", got)
	}
}

func TestPrepareCADockerfileRejects(t *testing.T) {
	t.Parallel()
	ca := testBuildCA(t)
	images := map[string]fakeImage{
		"ubuntu:26.04":   {},
		"node:20":        {onBuild: []string{"COPY . /app"}},
		"busybox:1.36.1": {nonDebian: true},
	}
	tests := []struct {
		name       string
		dockerfile string
		want       []string
	}{
		{
			"ONBUILD trigger on a stage base",
			"FROM node:20 AS app\nRUN true\n",
			[]string{`stage "app"`, "node:20", "ONBUILD", "ca_bundle = false"},
		},
		{
			"ONBUILD trigger on a later stage",
			"FROM ubuntu:26.04\nFROM node:20\n",
			[]string{`stage "1"`, "ONBUILD"},
		},
		{
			"FROM scratch",
			"FROM ubuntu:26.04 AS build\nFROM scratch\nCOPY --from=build /a /a\n",
			[]string{`stage "1"`, "FROM scratch", "Debian/Ubuntu", "ca_bundle = false"},
		},
		{
			"non-Debian stage",
			"FROM busybox:1.36.1\nRUN true\n",
			[]string{`stage "0"`, "busybox:1.36.1", "supports Debian/Ubuntu build stages only", "ca_bundle = false"},
		},
		{
			"unused non-Debian stage before Debian final stage",
			"FROM busybox:1.36.1 AS unused\nFROM ubuntu:26.04\nRUN true\n",
			[]string{`stage "unused"`, "busybox:1.36.1", "supports Debian/Ubuntu build stages only"},
		},
		{
			"unsupported frontend",
			"# syntax=example.com/custom-frontend:1\nFROM ubuntu:26.04\n",
			[]string{"syntax", "ca_bundle"},
		},
		{
			"unresolvable base",
			"FROM ${BASE}\n",
			[]string{"FROM", "ca_bundle"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := &fakeEngine{images: images}
			_, err := prepareCADockerfile(context.Background(), e.client(t), []byte(tt.dockerfile), nil, ca)
			if err == nil {
				t.Fatal("prepareCADockerfile = nil error, want error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

// jailocCAEnv returns the environment of an image that jailoc injected CA
// trust into, with skip left out of the defaults.
func jailocCAEnv(skip ...string) []string {
	env := []string{"PATH=/usr/bin"}
	var active []string
	for _, k := range caEnvKeys() {
		if containsString(skip, k) {
			continue
		}
		v, _ := caDefaultEnvValue(k)
		env = append(env, k+"="+v)
		active = append(active, k)
	}
	return append(env, caEnvDefaultsVar+"="+strings.Join(active, ","))
}

func TestSplitBaseCAEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		env          []string
		wantDefaults []string
		wantExplicit []string
	}{
		{"no env", nil, nil, nil},
		{"without marker everything is explicit", []string{"SSL_CERT_FILE=" + guest.SystemCABundle, "PATH=/bin"}, nil, []string{"SSL_CERT_FILE", "PATH"}},
		{"listed and at default", []string{"SSL_CERT_FILE=" + guest.SystemCABundle, "UV_SYSTEM_CERTS=true", caEnvDefaultsVar + "=SSL_CERT_FILE,UV_SYSTEM_CERTS"}, []string{"SSL_CERT_FILE", "UV_SYSTEM_CERTS"}, nil},
		{"listed but changed", []string{"SSL_CERT_FILE=/x.pem", caEnvDefaultsVar + "=SSL_CERT_FILE"}, nil, []string{"SSL_CERT_FILE"}},
		{"listed but absent", []string{caEnvDefaultsVar + "=SSL_CERT_FILE"}, nil, nil},
		{"unlisted at default value", []string{"SSL_CERT_FILE=" + guest.SystemCABundle, "PIP_CERT=" + guest.SystemCABundle, caEnvDefaultsVar + "=PIP_CERT"}, []string{"PIP_CERT"}, []string{"SSL_CERT_FILE"}},
		{"listed non-CA variable", []string{"PATH=/bin", caEnvDefaultsVar + "=PATH"}, nil, []string{"PATH"}},
	}
	keys := func(m map[string]bool) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			explicit, defaults := splitBaseCAEnv(tt.env)
			wantD, wantE := slices.Clone(tt.wantDefaults), slices.Clone(tt.wantExplicit)
			slices.Sort(wantD)
			slices.Sort(wantE)
			if got := keys(defaults); !slices.Equal(got, wantD) {
				t.Errorf("defaults = %v, want %v", got, wantD)
			}
			if got := keys(explicit); !slices.Equal(got, wantE) {
				t.Errorf("explicit = %v, want %v", got, wantE)
			}
		})
	}
}

func TestCATrustInstructionsRecordsDefaults(t *testing.T) {
	t.Parallel()

	got := caTrustInstructions("abcd", "", nil, map[string]bool{"PIP_CERT": true})
	var active []string
	for _, k := range caEnvKeys() {
		if k != "PIP_CERT" {
			active = append(active, k)
		}
	}
	if want := "ENV " + caEnvDefaultsVar + "=" + strings.Join(active, ",") + "\n"; !strings.Contains(got, want) {
		t.Errorf("instructions lack %q:\n%s", want, got)
	}

	// Defaults of the base are kept, not repeated; an ARG removes its key
	// from the recorded list.
	got = caTrustInstructions("abcd", "", jailocCAEnv(), map[string]bool{"SSL_CERT_FILE": true})
	if n := strings.Count(got, "ENV "); n != 1 {
		t.Errorf("instructions have %d ENV lines, want only the updated list:\n%s", n, got)
	}
	if strings.Contains(got, "ENV SSL_CERT_FILE=") || recordedDefault(got, "SSL_CERT_FILE") {
		t.Errorf("SSL_CERT_FILE is still a recorded default:\n%s", got)
	}

	// Nothing changed: the base's list stands.
	if got := caTrustInstructions("abcd", "", jailocCAEnv(), nil); strings.Contains(got, "ENV ") {
		t.Errorf("unchanged defaults produce ENV lines:\n%s", got)
	}
}

// recordedDefault reports whether the last list of defaults in dockerfile
// names key.
func recordedDefault(dockerfile, key string) bool {
	at := strings.LastIndex(dockerfile, "ENV "+caEnvDefaultsVar+"=")
	if at < 0 {
		return false
	}
	_, list, _ := strings.Cut(dockerfile[at:], "=")
	list, _, _ = strings.Cut(list, "\n")
	return slices.Contains(strings.Split(list, ","), key)
}

// envAfter reports whether the Dockerfile has line directly after an
// instruction that starts with prev.
func envAfter(dockerfile, prev, line string) bool {
	lines := strings.Split(dockerfile, "\n")
	for i := 0; i+1 < len(lines); i++ {
		if strings.HasPrefix(lines[i], prev) && lines[i+1] == line {
			return true
		}
	}
	return false
}

func TestPrepareCADockerfileArgOverridesInheritedDefault(t *testing.T) {
	t.Parallel()
	ca := testBuildCA(t)
	const override = `ENV SSL_CERT_FILE="/custom/arg.pem"`

	t.Run("descendant stage of the same Dockerfile", func(t *testing.T) {
		t.Parallel()
		e := &fakeEngine{images: map[string]fakeImage{"ubuntu:26.04": {}}}
		dockerfile := []byte("FROM ubuntu:26.04 AS parent\nFROM parent AS child\nARG SSL_CERT_FILE=/custom/arg.pem\nRUN true\nFROM child\nRUN true\n")
		got, err := prepareCADockerfile(context.Background(), e.client(t), dockerfile, nil, ca)
		if err != nil {
			t.Fatalf("prepareCADockerfile: %v", err)
		}
		text := string(got)
		if !envAfter(text, "ARG SSL_CERT_FILE=", override) {
			t.Errorf("ENV does not follow the ARG:\n%s", text)
		}
		if n := strings.Count(text, override); n != 1 {
			t.Errorf("override appears %d times, want once:\n%s", n, text)
		}
		// The child records that SSL_CERT_FILE is no longer a default, right
		// after its FROM.
		var rest []string
		for _, k := range caEnvKeys() {
			if k != "SSL_CERT_FILE" {
				rest = append(rest, k)
			}
		}
		if want := "FROM parent AS child\nENV " + caEnvDefaultsVar + "=" + strings.Join(rest, ",") + "\n"; !strings.Contains(text, want) {
			t.Errorf("child lacks the updated list %q:\n%s", want, text)
		}
		// The grandchild inherits from child: no further override.
		if strings.Count(text, "ENV "+caEnvDefaultsVar+"=") != 2 {
			t.Errorf("want one list for the parent and one for the child:\n%s", text)
		}
	})

	t.Run("descendant stage that sets the variable itself", func(t *testing.T) {
		t.Parallel()
		e := &fakeEngine{images: map[string]fakeImage{"ubuntu:26.04": {}}}
		dockerfile := []byte("FROM ubuntu:26.04 AS parent\nFROM parent\nENV SSL_CERT_FILE=/own.pem\nARG SSL_CERT_FILE=/custom/arg.pem\n")
		got, err := prepareCADockerfile(context.Background(), e.client(t), dockerfile, nil, ca)
		if err != nil {
			t.Fatalf("prepareCADockerfile: %v", err)
		}
		if strings.Contains(string(got), override) {
			t.Errorf("an ENV of the stage must win over the later ARG:\n%s", got)
		}
	})

	t.Run("descendant stage without a default to override", func(t *testing.T) {
		t.Parallel()
		e := &fakeEngine{images: map[string]fakeImage{"ubuntu:26.04": {}}}
		// The parent's own ARG keeps jailoc from defaulting SSL_CERT_FILE.
		dockerfile := []byte("FROM ubuntu:26.04 AS parent\nARG SSL_CERT_FILE=/parent.pem\nFROM parent\nARG SSL_CERT_FILE=/custom/arg.pem\n")
		got, err := prepareCADockerfile(context.Background(), e.client(t), dockerfile, nil, ca)
		if err != nil {
			t.Fatalf("prepareCADockerfile: %v", err)
		}
		if strings.Contains(string(got), "ENV SSL_CERT_FILE") {
			t.Errorf("no ENV expected for SSL_CERT_FILE:\n%s", got)
		}
	})

	t.Run("separately built layer", func(t *testing.T) {
		t.Parallel()
		e := &fakeEngine{images: map[string]fakeImage{"jailoc-base:1": {env: jailocCAEnv()}}}
		dockerfile := []byte("FROM jailoc-base:1\nARG SSL_CERT_FILE=/custom/arg.pem\nARG PIP_CERT\nRUN true\n")
		got, err := prepareCADockerfile(context.Background(), e.client(t), dockerfile, nil, ca)
		if err != nil {
			t.Fatalf("prepareCADockerfile: %v", err)
		}
		text := string(got)
		if !envAfter(text, "ARG SSL_CERT_FILE=", override) {
			t.Errorf("ENV does not follow the ARG:\n%s", text)
		}
		if strings.Contains(text, "ENV PIP_CERT") || strings.Contains(text, "ENV REQUESTS_CA_BUNDLE") {
			t.Errorf("defaults the base already has are repeated:\n%s", text)
		}
		if recordedDefault(text, "SSL_CERT_FILE") {
			t.Errorf("SSL_CERT_FILE is still a recorded default:\n%s", text)
		}
	})

	t.Run("separately built layer takes a build argument", func(t *testing.T) {
		t.Parallel()
		e := &fakeEngine{images: map[string]fakeImage{"jailoc-base:1": {env: jailocCAEnv()}}}
		value := "/from/$build arg.pem"
		dockerfile := []byte("FROM jailoc-base:1\nARG SSL_CERT_FILE\nRUN true\n")
		got, err := prepareCADockerfile(context.Background(), e.client(t), dockerfile, map[string]*string{"SSL_CERT_FILE": &value}, ca)
		if err != nil {
			t.Fatalf("prepareCADockerfile: %v", err)
		}
		if want := `ENV SSL_CERT_FILE="/from/\$build arg.pem"`; !envAfter(string(got), "ARG SSL_CERT_FILE", want) {
			t.Errorf("want %q after the ARG:\n%s", want, got)
		}
	})

	t.Run("explicit ENV of the base stays", func(t *testing.T) {
		t.Parallel()
		// No list: the base set SSL_CERT_FILE itself, even at jailoc's value.
		e := &fakeEngine{images: map[string]fakeImage{
			"explicit-base:1": {env: []string{"SSL_CERT_FILE=/explicit/base.pem"}},
			"listless:1":      {env: []string{"SSL_CERT_FILE=" + guest.SystemCABundle}},
		}}
		for _, base := range []string{"explicit-base:1", "listless:1"} {
			dockerfile := []byte("FROM " + base + "\nARG SSL_CERT_FILE=/custom/arg.pem\nRUN true\n")
			got, err := prepareCADockerfile(context.Background(), e.client(t), dockerfile, nil, ca)
			if err != nil {
				t.Fatalf("prepareCADockerfile(%s): %v", base, err)
			}
			if strings.Contains(string(got), "ENV SSL_CERT_FILE") {
				t.Errorf("%s: explicit SSL_CERT_FILE must stay:\n%s", base, got)
			}
		}
	})

	t.Run("ARG after RUN keeps early default and overrides after ARG", func(t *testing.T) {
		t.Parallel()
		e := &fakeEngine{images: map[string]fakeImage{"ubuntu:26.04": {}}}
		dockerfile := []byte("FROM ubuntu:26.04\nRUN true\nARG REQUESTS_CA_BUNDLE=/custom/req.pem\nARG CURL_CA_BUNDLE=/custom/curl.pem\nRUN true\n")
		got, err := prepareCADockerfile(context.Background(), e.client(t), dockerfile, nil, ca)
		if err != nil {
			t.Fatalf("prepareCADockerfile: %v", err)
		}
		text := string(got)
		if !strings.Contains(text, "ENV REQUESTS_CA_BUNDLE="+guest.SystemCABundle+"\n") || !strings.Contains(text, "ENV CURL_CA_BUNDLE="+guest.SystemCABundle+"\n") {
			t.Errorf("early RUN lacks the initial defaults:\n%s", text)
		}
		if !envAfter(text, "ARG REQUESTS_CA_BUNDLE=", `ENV REQUESTS_CA_BUNDLE="/custom/req.pem"`) {
			t.Errorf("override does not follow ARG REQUESTS_CA_BUNDLE:\n%s", text)
		}
		if !envAfter(text, "ARG CURL_CA_BUNDLE=", `ENV CURL_CA_BUNDLE="/custom/curl.pem"`) {
			t.Errorf("override does not follow ARG CURL_CA_BUNDLE:\n%s", text)
		}
		if recordedDefault(text, "REQUESTS_CA_BUNDLE") || recordedDefault(text, "CURL_CA_BUNDLE") {
			t.Errorf("overridden keys are still recorded as active defaults:\n%s", text)
		}
	})

	t.Run("ARG default expands base image and parent stage ENV with ENV precedence", func(t *testing.T) {
		t.Parallel()
		baseEnv := append(jailocCAEnv(), "CA_DIR=/opt/company")
		e := &fakeEngine{images: map[string]fakeImage{
			"jailoc-base:1": {
				env: baseEnv,
			},
			"ubuntu:26.04": {},
		}}
		layer := []byte("FROM jailoc-base:1\nARG CA_DIR=/wrong\nARG SSL_CERT_FILE=${CA_DIR}/ca.pem\nRUN true\n")
		got, err := prepareCADockerfile(context.Background(), e.client(t), layer, nil, ca)
		if err != nil {
			t.Fatalf("prepareCADockerfile(layer): %v", err)
		}
		if want := `ENV SSL_CERT_FILE="/opt/company/ca.pem"`; !envAfter(string(got), "ARG SSL_CERT_FILE=", want) {
			t.Errorf("layer want %q after ARG SSL_CERT_FILE:\n%s", want, got)
		}

		multistage := []byte("FROM ubuntu:26.04 AS parent\nENV CA_DIR=/opt/parent\nFROM parent\nARG CA_DIR=/wrong\nARG REQUESTS_CA_BUNDLE=${CA_DIR}/req.pem\nRUN true\n")
		got, err = prepareCADockerfile(context.Background(), e.client(t), multistage, nil, ca)
		if err != nil {
			t.Fatalf("prepareCADockerfile(multistage): %v", err)
		}
		if want := `ENV REQUESTS_CA_BUNDLE="/opt/parent/req.pem"`; !envAfter(string(got), "ARG REQUESTS_CA_BUNDLE=", want) {
			t.Errorf("multistage want %q after ARG REQUESTS_CA_BUNDLE:\n%s", want, got)
		}
	})
}

func TestPrepareCADockerfileRemovesProbeVolumes(t *testing.T) {
	t.Parallel()
	e := &fakeEngine{images: map[string]fakeImage{"ubuntu:26.04": {}, "debian:12": {}}}
	_, err := prepareCADockerfile(context.Background(), e.client(t), []byte("FROM ubuntu:26.04\nFROM debian:12\n"), nil, testBuildCA(t))
	if err != nil {
		t.Fatalf("prepareCADockerfile: %v", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.removals) != 2 {
		t.Fatalf("containers removed = %d, want 2", len(e.removals))
	}
	for i, withVolumes := range e.removals {
		if !withVolumes {
			t.Errorf("removal %d did not ask for the anonymous volumes to go (v=1)", i)
		}
	}
}
