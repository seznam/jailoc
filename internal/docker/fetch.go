package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type sourceType int

const (
	sourceLocal sourceType = iota
	sourceHTTP
)

func detectSourceType(value string) (sourceType, error) {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~") {
		return sourceLocal, nil
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return sourceHTTP, nil
	}
	return 0, fmt.Errorf("unsupported dockerfile source %q: must be an absolute local path (/..., ~/...) or HTTP(S) URL", value)
}

func readLocalDockerfile(path string) ([]byte, error) {
	limit := int64(1 << 20)

	f, err := os.Open(path) //nolint:gosec // path is validated (absolute or tilde-expanded) in config.Validate before reaching here
	if err != nil {
		return nil, fmt.Errorf("open dockerfile %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	lr := io.LimitReader(f, limit+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("read dockerfile %q: %w", path, err)
	}

	if int64(len(data)) > limit {
		return nil, fmt.Errorf("dockerfile at %q exceeds 1MiB limit", path)
	}

	return data, nil
}

func loadDockerfile(ctx context.Context, source string) ([]byte, error) {
	st, err := detectSourceType(source)
	if err != nil {
		return nil, fmt.Errorf("load dockerfile: %w", err)
	}

	switch st {
	case sourceLocal:
		return readLocalDockerfile(source)
	case sourceHTTP:
		return fetchDockerfile(ctx, source)
	default:
		return nil, fmt.Errorf("load dockerfile: unknown source type for %q", source)
	}
}

func fetchDockerfile(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create fetch request for %s: %w", rawURL, err)
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Do(req) //nolint:gosec // URL is validated (scheme + host) in config.Validate before reaching here
	if err != nil {
		return nil, fmt.Errorf("execute fetch request to %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch dockerfile from %s: unexpected status %d", rawURL, resp.StatusCode)
	}

	limit := int64(1 << 20)
	lr := io.LimitReader(resp.Body, limit+1)

	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("read dockerfile body from %s: %w", rawURL, err)
	}

	if int64(len(data)) > limit {
		return nil, fmt.Errorf("dockerfile at %s exceeds 1MiB limit", rawURL)
	}

	return data, nil
}
