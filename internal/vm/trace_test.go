package vm

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// captureSlog swaps the default logger, so tests using it cannot run in parallel.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestStepTracerLogsSpans(t *testing.T) {
	tests := []struct {
		name    string
		run     func(trace.Span)
		sleep   bool
		want    []string
		notWant []string
		empty   bool
	}{
		{
			name: "attributes from start and set",
			run: func(s trace.Span) {
				s.SetAttributes(attribute.Bool("microvm.image.from_cache", true))
			},
			sleep: true,
			want:  []string{`level=INFO`, `msg="vm step finished"`, `step=microvm.ImagePull`, `microvm.image_ref=img:1`, `microvm.image.from_cache=true`},
		},
		{
			name: "repeated attribute keeps last value",
			run: func(s trace.Span) {
				s.SetAttributes(attribute.Bool("microvm.image.layered", true))
				s.SetAttributes(attribute.Bool("microvm.image.layered", false))
			},
			sleep:   true,
			want:    []string{`microvm.image.layered=false`},
			notWant: []string{`microvm.image.layered=true`},
		},
		{
			name: "recorded error",
			run: func(s trace.Span) {
				s.RecordError(errors.New("pull failed"))
				s.SetStatus(codes.Error, "pull failed")
			},
			want: []string{`level=WARN`, `msg="vm step failed"`, `step=microvm.ImagePull`, `microvm.image_ref=img:1`, `error="pull failed"`},
		},
		{
			name: "error status without recorded error",
			run: func(s trace.Span) {
				s.SetStatus(codes.Error, "ownership preparation incomplete")
			},
			want: []string{`level=WARN`, `msg="vm step failed"`, `error="ownership preparation incomplete"`},
		},
		{
			name: "advisory error without error status",
			run: func(s trace.Span) {
				s.RecordError(errors.New("process lacks CAP_CHOWN"))
			},
			want: []string{`level=WARN`, `msg="vm step finished"`, `error="process lacks CAP_CHOWN"`},
		},
		{
			name:  "instant success is skipped",
			run:   func(trace.Span) {},
			empty: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Fake time in the bubble: only the sleep advances the clock, so
			// span durations are exact.
			synctest.Test(t, func(t *testing.T) {
				buf := captureSlog(t)
				_, span := stepTracer{}.Tracer("test").Start(context.Background(), "microvm.ImagePull",
					trace.WithAttributes(attribute.String("microvm.image_ref", "img:1")))
				tt.run(span)
				if tt.sleep {
					time.Sleep(2 * time.Millisecond)
				}
				span.End()

				got := buf.String()
				if tt.empty {
					if got != "" {
						t.Fatalf("expected no log output, got %q", got)
					}
					return
				}
				for _, w := range tt.want {
					if !strings.Contains(got, w) {
						t.Errorf("log %q does not contain %q", got, w)
					}
				}
				for _, w := range tt.notWant {
					if strings.Contains(got, w) {
						t.Errorf("log %q contains %q", got, w)
					}
				}
			})
		})
	}
}
