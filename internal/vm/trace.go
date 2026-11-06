package vm

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// stepTracer turns the OpenTelemetry spans go-microvm records around each
// boot step (image pull, rootfs clone, hooks, VM start) into jailoc.log
// lines with their durations, attributes and errors.
type stepTracer struct{ noop.TracerProvider }

func (stepTracer) Tracer(string, ...trace.TracerOption) trace.Tracer { return stepSpans{} }

type stepSpans struct{ noop.Tracer }

func (stepSpans) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	cfg := trace.NewSpanStartConfig(opts...)
	return ctx, &stepSpan{name: name, start: time.Now(), attrs: cfg.Attributes()}
}

type stepSpan struct {
	noop.Span
	name  string
	start time.Time

	mu     sync.Mutex
	attrs  []attribute.KeyValue
	err    error
	status string
	failed bool
}

func (s *stepSpan) SetAttributes(kv ...attribute.KeyValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attrs = append(s.attrs, kv...)
}

func (s *stepSpan) RecordError(err error, _ ...trace.EventOption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *stepSpan) SetStatus(code codes.Code, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = code == codes.Error
	s.status = description
}

func (s *stepSpan) End(...trace.SpanEndOption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := time.Since(s.start).Round(time.Millisecond)
	args := make([]any, 0, 4+2*len(s.attrs)+2)
	args = append(args, "step", s.name, "duration", d)
	for _, kv := range s.attrs {
		args = append(args, string(kv.Key), kv.Value.AsInterface())
	}
	switch {
	case s.failed && s.err != nil:
		slog.Warn("vm step failed", append(args, "error", s.err)...)
	case s.failed:
		slog.Warn("vm step failed", append(args, "error", s.status)...)
	case s.err != nil:
		// Advisory failures, e.g. a non-required preflight check, record an
		// error without an error status: the boot went on.
		slog.Warn("vm step finished", append(args, "error", s.err)...)
	case d == 0:
		return // checks and lookups that take no time are noise
	default:
		slog.Info("vm step finished", args...)
	}
}
