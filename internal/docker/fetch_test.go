package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchDockerfile(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/success":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("FROM ubuntu:24.04\nRUN echo 'success'"))
		case "/notfound":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
		case "/servererror":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("internal server error"))
		case "/empty":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(""))
		case "/toolarge":
			w.WriteHeader(http.StatusOK)
			data := make([]byte, 1<<20+10)
			_, _ = w.Write(data)
		case "/exactlimit":
			w.WriteHeader(http.StatusOK)
			data := make([]byte, 1<<20)
			_, _ = w.Write(data)
		case "/timeout":
			time.Sleep(100 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(ts.Close)

	tests := []struct {
		name    string
		path    string
		ctxFn   func() (context.Context, context.CancelFunc)
		wantErr bool
		errText string
	}{
		{
			name:    "success",
			path:    "/success",
			ctxFn:   func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantErr: false,
		},
		{
			name:    "not found",
			path:    "/notfound",
			ctxFn:   func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantErr: true,
			errText: "unexpected status 404",
		},
		{
			name:    "server error",
			path:    "/servererror",
			ctxFn:   func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantErr: true,
			errText: "unexpected status 500",
		},
		{
			name:    "empty body",
			path:    "/empty",
			ctxFn:   func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantErr: false,
		},
		{
			name:    "too large body",
			path:    "/toolarge",
			ctxFn:   func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantErr: true,
			errText: "exceeds 1MiB limit",
		},
		{
			name:    "exact limit body",
			path:    "/exactlimit",
			ctxFn:   func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantErr: false,
		},
		{
			name: "context cancelled",
			path: "/success",
			ctxFn: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			wantErr: true,
			errText: "execute fetch request to",
		},
		{
			name: "context timeout",
			path: "/timeout",
			ctxFn: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 10*time.Millisecond)
			},
			wantErr: true,
			errText: "execute fetch request to",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := tt.ctxFn()
			defer cancel()

			data, err := fetchDockerfile(ctx, ts.URL+tt.path)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tt.errText != "" && !strings.Contains(err.Error(), tt.errText) {
					t.Errorf("expected error containing %q, got %q", tt.errText, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.path == "/success" && !strings.Contains(string(data), "FROM ubuntu:24.04") {
					t.Errorf("expected body to contain FROM ubuntu:24.04, got %q", string(data))
				}
				if tt.path == "/empty" && len(data) != 0 {
					t.Errorf("expected empty body, got %d bytes", len(data))
				}
			}
		})
	}
}
