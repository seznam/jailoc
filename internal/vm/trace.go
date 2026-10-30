package vm

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// stepTracer turns the OpenTelemetry spans go-microvm records around each
// boot step (image pull, rootfs clone, hooks, VM start) into jailoc.log
// lines with their durations.
type stepTracer struct{ noop.TracerProvider }

func (stepTracer) Tracer(string, ...trace.TracerOption) trace.Tracer { return stepSpans{} }

type stepSpans struct{ noop.Tracer }

func (stepSpans) Start(ctx context.Context, name string, _ ...trace.SpanStartOption) (context.Context, trace.Span) {
	return ctx, &stepSpan{name: name, start: time.Now()}
}

type stepSpan struct {
	noop.Span
	name  string
	start time.Time
}

func (s *stepSpan) End(...trace.SpanEndOption) {
	d := time.Since(s.start).Round(time.Millisecond)
	if d == 0 {
		return // checks and lookups that take no time are noise
	}
	slog.Info("vm step finished", "step", s.name, "duration", d)
}
