package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

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
