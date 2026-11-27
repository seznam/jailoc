package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
	"github.com/moby/moby/api/types/container"
	dockerclient "github.com/moby/moby/client"

	"github.com/seznam/jailoc/internal/guest"
)

// caFrontendRef matches the standard Dockerfile frontend: the tag (group 1)
// and the digest (group 2) are both optional, but one of them must be there.
var caFrontendRef = regexp.MustCompile(`^(?:docker\.io/)?docker/dockerfile(?::([A-Za-z0-9._-]+))?(@sha256:[0-9a-f]{64})?$`)

// caFrontendVersion matches the numbered tags of the frontend, 1.<minor>[.<patch>][-labs].
var caFrontendVersion = regexp.MustCompile(`^1\.(\d+)(?:\.\d+)?(?:-labs)?$`)

// checkCAFrontend rejects Dockerfiles whose # syntax directive selects a
// frontend that may not support BuildKit secret mounts. No directive is fine:
// the builder's own frontend is used.
func checkCAFrontend(dockerfile []byte) error {
	syntax, _, _, _ := parser.DetectSyntax(dockerfile)
	if syntax == "" {
		return nil
	}
	if caFrontendSupportsSecrets(syntax) {
		return nil
	}
	return fmt.Errorf("dockerfile syntax %q: ca_bundle forwarding requires the standard docker/dockerfile frontend (1.2 or later); remove # syntax or set ca_bundle = false", syntax)
}

func caFrontendSupportsSecrets(syntax string) bool {
	m := caFrontendRef.FindStringSubmatch(syntax)
	if m == nil {
		return false
	}
	tag, digest := m[1], m[2]
	if tag == "" {
		return digest != ""
	}
	switch tag {
	case "1", "1-labs", "latest":
		return true
	}
	v := caFrontendVersion.FindStringSubmatch(tag)
	if v == nil {
		return false
	}
	minor, err := strconv.Atoi(v[1])
	return err == nil && minor >= 2
}

// caStage is an external build stage that receives the CA trust steps.
type caStage struct {
	// fromEndLine is the 1-based last line of the stage's FROM instruction.
	fromEndLine int
	// base is the expanded base image reference.
	base string
	// name is the stage alias, or its index, for error messages.
	name string
	// argKeys are the names of the ARG instructions declared in the stage.
	argKeys map[string]bool
}

// caPlan is the CA trust plan of a Dockerfile.
type caPlan struct {
	// stages are the external stages that get CA trust.
	stages []caStage
	// images are the deduplicated external images the build pulls, in order
	// of appearance.
	images []string
}

// lexEnv is a shell.EnvGetter over a map.
type lexEnv map[string]string

func (e lexEnv) Get(key string) (string, bool) {
	v, ok := e[key]
	return v, ok
}

func (e lexEnv) Keys() []string {
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	return keys
}

// caPlanner holds the state of one planCATrust run.
type caPlanner struct {
	lex       *shell.Lex
	buildArgs map[string]*string
	meta      lexEnv
	known     map[string]bool
	// runFlags are the flags of the RUN instructions, by the 1-based first
	// line of the instruction.
	runFlags map[int][]string
	plan     caPlan
}

// planCATrust finds the stages of dockerfile that need CA trust steps and the
// external images the build uses. buildArgs are the build arguments of the
// build; they override the defaults of the global ARG instructions.
func planCATrust(dockerfile []byte, buildArgs map[string]*string) (caPlan, error) {
	res, err := parser.Parse(bytes.NewReader(dockerfile))
	if err != nil {
		return caPlan{}, fmt.Errorf("parse dockerfile: %w", err)
	}
	stages, metaArgs, err := instructions.Parse(res.AST, nil)
	if err != nil {
		return caPlan{}, fmt.Errorf("parse dockerfile instructions: %w", err)
	}

	p := &caPlanner{
		lex:       shell.NewLex(res.EscapeToken),
		buildArgs: buildArgs,
		meta: lexEnv{
			"BUILDPLATFORM":  "linux/" + runtime.GOARCH,
			"TARGETPLATFORM": "linux/" + runtime.GOARCH,
			"BUILDOS":        "linux",
			"TARGETOS":       "linux",
			"BUILDARCH":      runtime.GOARCH,
			"TARGETARCH":     runtime.GOARCH,
		},
		known:    make(map[string]bool),
		runFlags: runFlagsByLine(res.AST),
	}
	for _, a := range metaArgs {
		for _, kv := range a.Args {
			if v, ok := p.argValue(kv, p.meta); ok {
				p.meta[kv.Key] = v
			}
		}
	}
	for i, st := range stages {
		if err := p.addStage(i, st); err != nil {
			return caPlan{}, err
		}
	}
	return p.plan, nil
}

// argValue returns the value an ARG instruction gives its variable: the build
// argument, else its default expanded in env.
func (p *caPlanner) argValue(kv instructions.KeyValuePairOptional, env lexEnv) (string, bool) {
	if v := p.buildArgs[kv.Key]; v != nil {
		return *v, true
	}
	if kv.Value == nil {
		return "", false
	}
	v, _, err := p.lex.ProcessWord(*kv.Value, env)
	if err != nil {
		return *kv.Value, true
	}
	return v, true
}

// expand expands word in env and fails when it names a variable that has no
// value, which BuildKit would silently expand to nothing.
func (p *caPlanner) expand(word string, env lexEnv) (string, error) {
	res, err := p.lex.ProcessWordWithMatches(word, env)
	if err != nil {
		return "", err
	}
	for name := range res.Unmatched {
		if hasDefaultModifier(word, name) {
			continue
		}
		return "", fmt.Errorf("variable %q has no value", name)
	}
	if strings.Contains(res.Result, "$") {
		return "", fmt.Errorf("%q still contains an unexpanded variable", res.Result)
	}
	return res.Result, nil
}

// hasDefaultModifier reports whether word uses ${name:-...}, ${name-...},
// ${name:+...} or ${name+...}, which are fine for an unset variable.
func hasDefaultModifier(word, name string) bool {
	for _, mod := range []string{":-", "-", ":+", "+"} {
		if strings.Contains(word, "${"+name+mod) {
			return true
		}
	}
	return false
}

func (p *caPlanner) addStage(index int, st instructions.Stage) error {
	name := st.Name
	if name == "" {
		name = strconv.Itoa(index)
	}
	defer func() {
		if st.Name != "" {
			p.known[strings.ToLower(st.Name)] = true
		}
	}()

	base, err := p.expand(st.BaseName, p.meta)
	if err != nil {
		return fmt.Errorf("stage %q: cannot resolve FROM %s: %w; ca_bundle forwarding needs a resolvable base image: give the ARG a default or a build argument, or set ca_bundle = false", name, st.BaseName, err)
	}
	if base == "" {
		return fmt.Errorf("stage %q: FROM %s resolves to an empty image; ca_bundle forwarding needs a resolvable base image: give the ARG a default or a build argument, or set ca_bundle = false", name, st.BaseName)
	}

	lower := strings.ToLower(base)
	if lower == "scratch" {
		return fmt.Errorf("stage %q: FROM scratch cannot install ca_bundle trust (no shell or system trust store); use a Debian/Ubuntu base stage or set ca_bundle = false", name)
	}
	external := !p.known[lower]
	if external {
		if err := p.checkPlatform(name, st.Platform); err != nil {
			return err
		}
		p.addImage(base)
	}

	argKeys := make(map[string]bool)
	env := lexEnv{}
	for _, cmd := range st.Commands {
		switch c := cmd.(type) {
		case *instructions.ArgCommand:
			for _, kv := range c.Args {
				argKeys[kv.Key] = true
				if v, ok := p.argValue(kv, env); ok {
					env[kv.Key] = v
				} else if v, ok := p.meta[kv.Key]; ok {
					env[kv.Key] = v
				}
			}
		case *instructions.EnvCommand:
			for _, kv := range c.Env {
				v, _, err := p.lex.ProcessWord(kv.Value, env)
				if err != nil {
					v = kv.Value
				}
				env[kv.Key] = v
			}
		case *instructions.CopyCommand:
			if err := p.addFrom(name, c.From, env); err != nil {
				return err
			}
		case *instructions.RunCommand:
			if err := p.addMounts(name, c, env); err != nil {
				return err
			}
		}
	}

	if !external {
		return nil
	}
	if len(st.Location) == 0 {
		return fmt.Errorf("stage %q: no source location for its FROM instruction", name)
	}
	p.plan.stages = append(p.plan.stages, caStage{
		fromEndLine: st.Location[len(st.Location)-1].End.Line,
		base:        base,
		name:        name,
		argKeys:     argKeys,
	})
	return nil
}

// checkPlatform rejects a FROM --platform the CA trust steps can't serve: the
// build runs them on the workspace's own Linux architecture.
func (p *caPlanner) checkPlatform(stage, platform string) error {
	if platform == "" {
		return nil
	}
	got, err := p.expand(platform, p.meta)
	if err != nil {
		return fmt.Errorf("stage %q: cannot resolve FROM --platform=%s: %w; ca_bundle forwarding supports only linux/%s builds: set ca_bundle = false", stage, platform, err, runtime.GOARCH)
	}
	if got == "" || got == "linux" || got == "linux/"+runtime.GOARCH {
		return nil
	}
	return fmt.Errorf("stage %q: FROM --platform=%s: ca_bundle forwarding supports only linux/%s builds: remove --platform or set ca_bundle = false", stage, got, runtime.GOARCH)
}

// runFlagsByLine returns the flags of the RUN instructions of ast by the
// 1-based first line of each instruction.
func runFlagsByLine(ast *parser.Node) map[int][]string {
	flags := make(map[int][]string)
	for _, node := range ast.Children {
		if strings.EqualFold(node.Value, "run") && len(node.Flags) > 0 {
			flags[node.StartLine] = node.Flags
		}
	}
	return flags
}

// addMounts records the images the RUN mounts of c name with from=. It reads
// only the from= option of the raw --mount flags: the other options (uid, gid,
// mode, size, readonly, ...) may use variables of the base image's ENV, which
// are not known yet and which BuildKit expands itself.
func (p *caPlanner) addMounts(stage string, c *instructions.RunCommand, env lexEnv) error {
	loc := c.Location()
	if len(loc) == 0 {
		return nil
	}
	for _, from := range mountFromValues(p.runFlags[loc[0].Start.Line]) {
		got, err := p.expand(from, env)
		if err != nil {
			return fmt.Errorf("stage %q: cannot resolve RUN --mount from=%s: %w; ca_bundle forwarding needs resolvable images: give the ARG a default or a build argument, or set ca_bundle = false", stage, from, err)
		}
		if err := p.addFrom(stage, got, env); err != nil {
			return err
		}
	}
	return nil
}

// mountFromValues returns the unexpanded from= values of the --mount flags.
func mountFromValues(flags []string) []string {
	var froms []string
	for _, flag := range flags {
		spec, ok := strings.CutPrefix(flag, "--mount=")
		if !ok {
			continue
		}
		for _, field := range splitMountFields(spec) {
			if key, value, ok := strings.Cut(field, "="); ok && strings.EqualFold(strings.TrimSpace(key), "from") {
				froms = append(froms, value)
			}
		}
	}
	return froms
}

// splitMountFields splits a --mount value at its commas, CSV style: a double
// quote starts or ends a quoted section in which commas don't split, and ""
// inside it is a literal quote.
func splitMountFields(spec string) []string {
	var fields []string
	var cur strings.Builder
	quoted := false
	for i := 0; i < len(spec); i++ {
		ch := spec[i]
		switch {
		case ch == '"' && quoted && i+1 < len(spec) && spec[i+1] == '"':
			cur.WriteByte('"')
			i++
		case ch == '"':
			quoted = !quoted
		case ch == ',' && !quoted:
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(ch)
		}
	}
	return append(fields, cur.String())
}

// addFrom records the image a COPY --from or RUN --mount from= names, unless
// it names a stage.
func (p *caPlanner) addFrom(stage, ref string, env lexEnv) error {
	if ref == "" {
		return nil
	}
	got, err := p.expand(ref, env)
	if err != nil {
		return fmt.Errorf("stage %q: cannot resolve --from=%s: %w; ca_bundle forwarding needs resolvable images: give the ARG a default or a build argument, or set ca_bundle = false", stage, ref, err)
	}
	if got == "" || p.known[strings.ToLower(got)] || isStageIndex(got) || strings.EqualFold(got, "scratch") {
		return nil
	}
	p.addImage(got)
	return nil
}

func (p *caPlanner) addImage(ref string) {
	if !slices.Contains(p.plan.images, ref) {
		p.plan.images = append(p.plan.images, ref)
	}
}

func isStageIndex(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 0 && strconv.Itoa(n) == s
}

// caTrustScript installs the secret CA bundle into the stage's system trust
// store. It has no $, so BuildKit's variable expansion leaves it alone.
const caTrustScript = `if [ ! -f /etc/debian_version ]; then echo "jailoc: ca_bundle forwarding supports Debian/Ubuntu build stages only (missing /etc/debian_version)" >&2; exit 1; fi; ` +
	`mkdir -p /usr/local/share/ca-certificates /etc/ssl/certs && ` +
	`rm -f /usr/local/share/ca-certificates/jailoc-build-ca-*.crt && ` +
	`awk 'BEGIN { n = 0; out = "" } /-----BEGIN CERTIFICATE-----/ { n++; out = sprintf("/usr/local/share/ca-certificates/jailoc-build-ca-%03d.crt", n) } out != "" { print > out } /-----END CERTIFICATE-----/ { if (out != "") close(out); out = "" }' /run/secrets/` + caSecretID + ` && ` +
	`chmod 0644 /usr/local/share/ca-certificates/jailoc-build-ca-*.crt && ` +
	`if command -v update-ca-certificates >/dev/null 2>&1; then update-ca-certificates >/dev/null; else cat /usr/local/share/ca-certificates/jailoc-build-ca-*.crt >> /etc/ssl/certs/ca-certificates.crt; fi && ` +
	`chmod 0644 /etc/ssl/certs/ca-certificates.crt`

// caTrustInstructions returns the instructions that go right after a stage's
// FROM: a RUN that installs the CA bundle secret into the system trust store,
// and ENV lines that point TLS clients at it. baseUser is the user of the base
// image, restored after the RUN as the stage's user. baseEnv are the KEY=VALUE
// entries of the base image and argKeys the stage's ARG names; neither's
// variables are overridden. The digest sits literally in the RUN, so a changed
// bundle changes the step and its cache key.
func caTrustInstructions(digest, baseUser string, baseEnv []string, argKeys map[string]bool) string {
	var b strings.Builder
	switchUser := asRoot(baseUser)
	if switchUser {
		b.WriteString("USER root\n")
	}

	script := ": " + caTrustMarker + " sha256:" + digest + "; set -eu; " + caTrustScript
	var quoted bytes.Buffer
	enc := json.NewEncoder(&quoted)
	enc.SetEscapeHTML(false)
	// Encoding a string cannot fail.
	_ = enc.Encode(script)
	exec := `["/bin/sh", "-c", ` + strings.TrimSuffix(quoted.String(), "\n") + `]`
	b.WriteString("RUN --network=none --mount=type=secret,id=" + caSecretID + ",required=true " + exec + "\n")

	have := make(map[string]bool, len(baseEnv))
	for _, kv := range baseEnv {
		key, _, _ := strings.Cut(kv, "=")
		have[key] = true
	}
	writeEnv := func(key, value string) {
		if have[key] || argKeys[key] {
			return
		}
		b.WriteString("ENV " + key + "=" + value + "\n")
	}
	writeEnv("UV_SYSTEM_CERTS", "true")
	writeEnv("NODE_USE_SYSTEM_CA", "1")
	for _, key := range guest.CABundleVars {
		writeEnv(key, guest.SystemCABundle)
	}

	if switchUser {
		b.WriteString("USER " + baseUser + "\n")
	}
	return b.String()
}

// injectAfterLines returns dockerfile with each insert placed after its
// 1-based line number. All other bytes stay untouched, so heredocs,
// directives, comments and line endings survive. Line numbers outside the
// file are ignored.
func injectAfterLines(dockerfile []byte, inserts map[int]string) []byte {
	out := make([]byte, 0, len(dockerfile)+len(inserts)*1024)
	rest := dockerfile
	for line := 1; len(rest) > 0; line++ {
		end := bytes.IndexByte(rest, '\n')
		terminated := end >= 0
		chunk := rest
		if terminated {
			chunk = rest[:end+1]
		}
		rest = rest[len(chunk):]
		out = append(out, chunk...)

		ins, ok := inserts[line]
		if !ok || ins == "" {
			continue
		}
		if !terminated {
			out = append(out, '\n')
		}
		out = append(out, ins...)
		if !strings.HasSuffix(ins, "\n") {
			out = append(out, '\n')
		}
	}
	return out
}

// ensureImageWithClient makes sure the daemon behind cli has imageRef for the
// VM platform. An image cli already has costs one inspect; anything else is
// left to EnsureBuildImage.
func ensureImageWithClient(ctx context.Context, cli dockerclient.APIClient, imageRef string, ca *BuildCA) error {
	if info, err := cli.ImageInspect(ctx, imageRef); err == nil && imagePlatform(info) == "linux/"+runtime.GOARCH {
		return nil
	}
	return EnsureBuildImage(ctx, imageRef, ca)
}

// checkDebianBase rejects a stage whose base image has no /etc/debian_version.
// It creates, without starting, a container of the image and looks the file
// up in its root filesystem.
func checkDebianBase(ctx context.Context, cli dockerclient.APIClient, st caStage) error {
	ctr, err := cli.ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Image:  st.base,
		Config: &container.Config{Cmd: []string{"true"}},
	})
	if err != nil {
		return fmt.Errorf("create container of image %s of stage %q to check for Debian/Ubuntu: %w", st.base, st.name, err)
	}
	defer func() {
		// Best effort: a leftover stopped container does not affect the build.
		_, _ = cli.ContainerRemove(context.WithoutCancel(ctx), ctr.ID, dockerclient.ContainerRemoveOptions{Force: true})
	}()

	resp, err := cli.CopyFromContainer(ctx, ctr.ID, dockerclient.CopyFromContainerOptions{SourcePath: "/etc/debian_version"})
	if err == nil {
		_ = resp.Content.Close()
		return nil
	}
	if cerrdefs.IsNotFound(err) || isMissingFileError(err) {
		return fmt.Errorf("stage %q (base %s): ca_bundle forwarding supports Debian/Ubuntu build stages only (missing /etc/debian_version); use a Debian/Ubuntu base image or set ca_bundle = false", st.name, st.base)
	}
	return fmt.Errorf("look up /etc/debian_version in image %s of stage %q: %w", st.base, st.name, err)
}

func isMissingFileError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such file") || strings.Contains(msg, "could not find the file")
}

// prepareCADockerfile returns dockerfile with the CA trust steps of ca after
// the FROM of each external stage, for a build with buildArgs on the daemon
// behind cli. It pulls the images of the build, if it lacks them, to read the
// user and environment of each stage base, and rejects bases whose ONBUILD
// triggers would run before the trust steps. A nil ca returns dockerfile as
// it is.
func prepareCADockerfile(ctx context.Context, cli dockerclient.APIClient, dockerfile []byte, buildArgs map[string]*string, ca *BuildCA) ([]byte, error) {
	if ca == nil {
		return dockerfile, nil
	}
	if err := checkCAFrontend(dockerfile); err != nil {
		return nil, err
	}
	plan, err := planCATrust(dockerfile, buildArgs)
	if err != nil {
		return nil, fmt.Errorf("plan ca_bundle trust: %w", err)
	}
	for _, img := range plan.images {
		if err := ensureImageWithClient(ctx, cli, img, ca); err != nil {
			return nil, fmt.Errorf("prepare image %s for ca_bundle trust: %w", img, err)
		}
	}

	inserts := make(map[int]string, len(plan.stages))
	for _, st := range plan.stages {
		// Every external stage is checked, even one BuildKit would never
		// run, so the result doesn't depend on which stages the build reaches.
		if err := checkDebianBase(ctx, cli, st); err != nil {
			return nil, err
		}
		inspect, err := cli.ImageInspect(ctx, st.base)
		if err != nil {
			return nil, fmt.Errorf("inspect image %s of stage %q: %w", st.base, st.name, err)
		}
		var baseUser string
		var baseEnv []string
		if inspect.Config != nil {
			if len(inspect.Config.OnBuild) > 0 {
				return nil, fmt.Errorf("stage %q (base %s): base image has ONBUILD triggers, which run before jailoc can install ca_bundle trust; remove ONBUILD from the base image or set ca_bundle = false", st.name, st.base)
			}
			baseUser = inspect.Config.User
			baseEnv = inspect.Config.Env
		}
		inserts[st.fromEndLine] = caTrustInstructions(ca.Digest(), baseUser, baseEnv, st.argKeys)
	}
	return injectAfterLines(dockerfile, inserts), nil
}
