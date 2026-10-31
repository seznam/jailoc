package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"regexp"

	"github.com/docker/docker/api/types/build"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	controlapi "github.com/moby/buildkit/api/services/control"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/progress/progressui"
)

// cleanCachesArg is set to 1 when jailoc builds its own Dockerfiles with the
// classic builder: without cache mounts, their RUN steps remove the caches
// themselves so that they do not end up in the image.
const cleanCachesArg = "JAILOC_CLEAN_CACHES"

// cacheMount matches a RUN cache mount flag together with the line
// continuation that may follow it.
var cacheMount = regexp.MustCompile(`--mount=type=cache\S*[ \t]*(\\\r?\n[ \t]*)?`)

// builderVersion returns BuildKit when the daemon reports it as its builder
// and the classic builder otherwise, e.g. for Docker-compatible APIs that do
// not implement BuildKit.
func builderVersion(ctx context.Context, cli dockerclient.APIClient) (build.BuilderVersion, error) {
	ping, err := cli.Ping(ctx)
	if err != nil {
		return "", fmt.Errorf("ping Docker daemon: %w", err)
	}
	if ping.BuilderVersion == build.BuilderBuildKit {
		return build.BuilderBuildKit, nil
	}
	slog.Debug("Docker daemon does not report BuildKit, using the classic builder", "builder", ping.BuilderVersion)
	return build.BuilderV1, nil
}

// forBuilder adapts a jailoc Dockerfile with cache mounts to version: the
// classic builder gets the Dockerfile without them and cleanCachesArg in args.
func forBuilder(version build.BuilderVersion, dockerfile []byte, args map[string]*string) []byte {
	if version == build.BuilderBuildKit {
		return dockerfile
	}
	clean := "1"
	args[cleanCachesArg] = &clean
	return cacheMount.ReplaceAll(dockerfile, nil)
}

// imageBuild builds buildCtx with the builder version and shows its progress
// on stderr.
func imageBuild(ctx context.Context, cli dockerclient.APIClient, buildCtx io.Reader, version build.BuilderVersion, opts build.ImageBuildOptions) error {
	opts.Version = version
	if version == build.BuilderBuildKit {
		// BuildKit resolves base images through a client session, as docker
		// build does; without attachables, registry access is anonymous.
		sess, err := session.NewSession(ctx, "")
		if err != nil {
			return fmt.Errorf("create BuildKit session: %w", err)
		}
		dial := func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
			return cli.DialHijack(ctx, "/session", proto, meta)
		}
		go func() {
			if err := sess.Run(ctx, dial); err != nil {
				slog.Debug("BuildKit session ended", "error", err)
			}
		}()
		defer func() { _ = sess.Close() }()
		opts.SessionID = sess.ID()
	}
	resp, err := cli.ImageBuild(ctx, buildCtx, opts)
	if err != nil {
		return fmt.Errorf("start build: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	display := displayStream
	if version == build.BuilderBuildKit {
		display = func(r io.Reader) error { return displayBuildKitStream(ctx, r) }
	}
	if err := display(resp.Body); err != nil {
		return fmt.Errorf("read build output: %w", err)
	}
	return nil
}

// displayBuildKitStream renders the BuildKit status updates that the daemon
// sends as moby.buildkit.trace messages like docker build does.
func displayBuildKitStream(ctx context.Context, r io.Reader) error {
	display, err := progressui.NewDisplay(os.Stderr, progressui.AutoMode)
	if err != nil {
		return fmt.Errorf("create build progress display: %w", err)
	}
	statuses := make(chan *bkclient.SolveStatus)
	displayDone := make(chan error, 1)
	go func() {
		// The display must drain statuses until the stream ends, even
		// when ctx is canceled.
		_, err := display.UpdateFrom(context.WithoutCancel(ctx), statuses)
		displayDone <- err
	}()
	streamErr := jsonmessage.DisplayJSONMessagesStream(r, io.Discard, 0, false, func(m jsonmessage.JSONMessage) {
		if m.ID != "moby.buildkit.trace" {
			return
		}
		var data []byte
		if err := json.Unmarshal(*m.Aux, &data); err != nil {
			slog.Debug("decode BuildKit status", "error", err)
			return
		}
		var status controlapi.StatusResponse
		if err := status.UnmarshalVT(data); err != nil {
			slog.Debug("decode BuildKit status", "error", err)
			return
		}
		statuses <- bkclient.NewSolveStatus(&status)
	})
	close(statuses)
	displayErr := <-displayDone
	if streamErr != nil {
		return streamErr
	}
	if displayErr != nil {
		return fmt.Errorf("display build progress: %w", displayErr)
	}
	return nil
}
