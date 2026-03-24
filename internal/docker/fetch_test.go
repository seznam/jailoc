package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDetectSourceType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input   string
		want    sourceType
		wantErr bool
	}{
		{"/tmp/Dockerfile", sourceLocal, false},
		{"~/Dockerfile", sourceLocal, false},
		{"/absolute/path/file", sourceLocal, false},
		{"https://example.com/Dockerfile", sourceHTTP, false},
		{"http://example.com/Dockerfile", sourceHTTP, false},
		{"", 0, true},
		{"relative/path", 0, true},
		{"ftp://bad.example.com/Dockerfile", 0, true},
		{"justAName", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got, err := detectSourceType(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("detectSourceType(%q): expected error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("detectSourceType(%q): unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("detectSourceType(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestReadLocalDockerfile(t *testing.T) {
	t.Parallel()
	content := []byte("FROM ubuntu:22.04\nRUN apt-get update\n")
	f, err := os.CreateTemp("", "test-dockerfile-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(f.Name()) }) //nolint:gosec // cleaning up temp file created in this test
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	got, err := readLocalDockerfile(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("got %q, want %q", got, content)
	}
}

func TestReadLocalDockerfileNotFound(t *testing.T) {
	t.Parallel()
	_, err := readLocalDockerfile("/nonexistent/path/Dockerfile")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestReadLocalDockerfileTooLarge(t *testing.T) {
	t.Parallel()
	f, err := os.CreateTemp("", "test-dockerfile-large-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(f.Name()) }) //nolint:gosec // cleaning up temp file created in this test
	data := make([]byte, (1<<20)+1)
	for i := range data {
		data[i] = 'A'
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	_, err = readLocalDockerfile(f.Name())
	if err == nil {
		t.Fatal("expected error for oversized file, got nil")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "too large") && !strings.Contains(errStr, "exceeds") && !strings.Contains(errStr, "1MiB") {
		t.Fatalf("expected error to mention size limit, got: %v", err)
	}
}

func TestLoadDockerfile(t *testing.T) {
	t.Parallel()

	content := []byte("FROM ubuntu:22.04\n")
	f, err := os.CreateTemp("", "test-dockerfile-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(f.Name()) }) //nolint:gosec // cleaning up temp file created in this test
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	got, err := loadDockerfile(t.Context(), f.Name())
	if err != nil {
		t.Fatalf("loadDockerfile(local): unexpected error: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("loadDockerfile(local): got %q, want %q", got, content)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("FROM alpine:3.18\n"))
	}))
	t.Cleanup(ts.Close)

	httpGot, err := loadDockerfile(t.Context(), ts.URL+"/Dockerfile")
	if err != nil {
		t.Fatalf("loadDockerfile(HTTP): unexpected error: %v", err)
	}
	if string(httpGot) != "FROM alpine:3.18\n" {
		t.Fatalf("loadDockerfile(HTTP): got %q, want %q", httpGot, "FROM alpine:3.18\n")
	}
}

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
