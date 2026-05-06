//go:build e2e

// Package e2e tests github.com/dio/log against a real OTLP sink.
//
// By default, an in-process OTLP gRPC sink is used, no Docker required,
// precise assertions (value, label, trace ID), no sleep.
//
// For human verification, set E2E_OTEL_FRONT=1 to additionally start otel-front
// (via Docker) and route all telemetry there so you can browse it at
// http://localhost:8000.
//
// Run:
//
//	cd e2e && go test -v -tags e2e -timeout 60s ./...
//
// With otel-front UI:
//
//	cd e2e && E2E_OTEL_FRONT=1 go test -v -tags e2e -timeout 90s ./...
package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/tetratelabs/telemetry"
	"github.com/tetratelabs/telemetry/scope"

	log "github.com/dio/log"
)

// ---------------------------------------------------------------------------
// Metric declarations
// ---------------------------------------------------------------------------

var (
	clusterLabel  telemetry.Label
	reserveOK     telemetry.Metric
	reserveErrors telemetry.Metric
)

func init() {
	telemetry.ToGlobalMetricSink(func(ms telemetry.MetricSink) {
		clusterLabel  = ms.NewLabel("cluster")
		reserveOK     = ms.NewSum("zia_quota_reserve_total", "Successful reservations")
		reserveErrors = ms.NewSum("zia_quota_reserve_errors_total", "Reserve errors")
	})
}

var quotaLog = scope.Register("quotasvc", "Quota service operations")

// ---------------------------------------------------------------------------
// Globals set in TestMain
// ---------------------------------------------------------------------------

var (
	sink   *Sink
	tp     *sdktrace.TracerProvider
	mp     *metric.MeterProvider
	lp     *sdklog.LoggerProvider
	tracer trace.Tracer
)

// ---------------------------------------------------------------------------
// TestMain
// ---------------------------------------------------------------------------

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Start in-process sink (always).
	var err error
	sink, err = NewSink()
	must("in-process sink", err)
	defer sink.Stop()

	otlpTarget := sink.Addr() // exporters point here by default

	// Optionally also start otel-front for human browsing.
	if os.Getenv("E2E_OTEL_FRONT") != "" {
		startOtelFront()
		defer stopOtelFront()
		// Fan-out not supported in standard OTel SDK; otel-front gets same data
		// by pointing exporters there instead. Set target to otel-front.
		otlpTarget = "localhost:4317"
		waitHTTPReady("http://localhost:8000/health", 20*time.Second)
		fmt.Fprintln(os.Stderr, "otel-front UI: http://localhost:8000")
	}

	res, _ := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName("zia-log-e2e")),
	)

	// Traces
	traceExp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(otlpTarget),
		otlptracegrpc.WithInsecure(),
	)
	must("trace exporter", err)
	tp = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	tracer = tp.Tracer("zia-e2e")

	// Metrics: 200ms flush so tests don't wait long.
	metricExp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(otlpTarget),
		otlpmetricgrpc.WithInsecure(),
	)
	must("metric exporter", err)
	mp = metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExp,
			metric.WithInterval(200*time.Millisecond))),
		metric.WithResource(res),
	)

	// Logs
	logExp, err := otlploggrpc.New(ctx,
		otlploggrpc.WithEndpoint(otlpTarget),
		otlploggrpc.WithInsecure(),
	)
	must("log exporter", err)
	lp = sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(logExp)),
		sdklog.WithResource(res),
	)

	// Wire telemetry library
	telemetry.SetGlobalMetricSink(log.NewOTelSink(mp, "zia"))
	scope.UseLogger(log.New(slog.New(&otelBridge{provider: lp})))

	code := m.Run()

	_ = tp.ForceFlush(ctx)
	_ = mp.ForceFlush(ctx)
	_ = lp.ForceFlush(ctx)
	_ = tp.Shutdown(ctx)
	_ = mp.Shutdown(ctx)
	_ = lp.Shutdown(ctx)

	os.Exit(code)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestWhenWeLogWeAlsoSendMetrics: one call emits both log + metric.
// The in-process sink validates exact values and labels, no sleep needed.
func TestWhenWeLogWeAlsoSendMetrics(t *testing.T) {
	sink.Reset()

	ctx, span := tracer.Start(context.Background(), "quota.Reserve")
	traceID := span.SpanContext().TraceID().String()

	quotaLog.Context(ctx).
		Metric(reserveOK.With(clusterLabel.Upsert("openai"))).
		Info("reserve success", "user_id", "alice", "tokens", 1000)

	quotaLog.Context(ctx).
		Metric(reserveErrors.With(clusterLabel.Upsert("anthropic"))).
		Error("reserve failed", context.DeadlineExceeded, "user_id", "bob")

	span.End()
	_ = tp.ForceFlush(ctx)
	_ = lp.ForceFlush(ctx) // flush log batch before asserting

	// Metrics: exact value + label, no sleep
	val, ok := sink.WaitForCounter("zia_quota_reserve_total", "cluster", "openai", 1, 5*time.Second)
	if !ok {
		t.Errorf("zia_quota_reserve_total{cluster=openai}: want >= 1, got %d", val)
	} else {
		t.Logf("zia_quota_reserve_total{cluster=openai} = %d", val)
	}

	val, ok = sink.WaitForCounter("zia_quota_reserve_errors_total", "cluster", "anthropic", 1, 5*time.Second)
	if !ok {
		t.Errorf("zia_quota_reserve_errors_total{cluster=anthropic}: want >= 1, got %d", val)
	} else {
		t.Logf("zia_quota_reserve_errors_total{cluster=anthropic} = %d", val)
	}

	// Logs: body + trace ID correlation (trace_id stored as attribute by the bridge)
	rec, ok := sink.WaitForLog("reserve success", 5*time.Second)
	if !ok {
		t.Error("log record 'reserve success' not received")
	} else {
		logTraceID := rec.Attrs["trace_id"] // bridge stores it as an attribute
		t.Logf("log: body=%q trace_id=%s", rec.Body, logTraceID)
		if logTraceID != traceID {
			t.Errorf("log trace_id mismatch: want %s, got %s", traceID, logTraceID)
		}
	}

	// Spans: by trace ID
	sp, ok := sink.WaitForSpan(traceID, 5*time.Second)
	if !ok {
		t.Errorf("span with trace_id=%s not received", traceID)
	} else {
		t.Logf("span: name=%q trace_id=%s", sp.Name, sp.TraceID)
	}
}

// TestMetricFiresEvenWhenLogIsSilenced: the key guarantee.
// Log is suppressed at Error level; metric still reaches the sink.
func TestMetricFiresEvenWhenLogIsSilenced(t *testing.T) {
	sink.Reset()

	var silenced telemetry.Metric
	telemetry.ToGlobalMetricSink(func(ms telemetry.MetricSink) {
		silenced = ms.NewSum("zia_silenced_events_total", "Info events when log silenced")
	})

	logger := log.New(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	logger.SetLevel(telemetry.LevelError) // Info silenced

	logger.Metric(silenced).Info("this log will NOT appear in output")

	val, ok := sink.WaitForCounter("zia_silenced_events_total", "", "", 1, 5*time.Second)
	if !ok {
		t.Errorf("metric fired despite log silence: want >= 1, got %d", val)
	} else {
		t.Logf("zia_silenced_events_total = %d (log was silent)", val)
	}
}

// ---------------------------------------------------------------------------
// otel-front Docker lifecycle (only when E2E_OTEL_FRONT=1)
// ---------------------------------------------------------------------------

const containerName = "otel-front-e2e"

func startOtelFront() {
	_ = exec.Command("docker", "rm", "-f", containerName).Run()
	cmd := exec.Command("docker", "run", "-d",
		"--name", containerName,
		"-p", "8000:8000",
		"-p", "4317:4317",
		"-p", "4318:4318",
		"ghcr.io/mesaglio/otel-front:latest",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "docker run otel-front: %v\n", err)
		os.Exit(1)
	}
}

func stopOtelFront() {
	_ = exec.Command("docker", "rm", "-f", containerName).Run()
}

func waitHTTPReady(url string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "otel-front not ready at %s after %s\n", url, timeout)
	os.Exit(1)
}

func must(label string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", label, err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// slog → OTel log bridge
// ---------------------------------------------------------------------------

type otelBridge struct {
	provider *sdklog.LoggerProvider
}

func (b *otelBridge) Handle(ctx context.Context, r slog.Record) error {
	logger := b.provider.Logger("zia-log")
	var rec otellog.Record
	rec.SetTimestamp(r.Time)
	rec.SetSeverityText(r.Level.String())
	rec.SetBody(otellog.StringValue(r.Message))
	r.Attrs(func(a slog.Attr) bool {
		rec.AddAttributes(otellog.String(a.Key, fmt.Sprint(a.Value.Any())))
		return true
	})
	// Propagate OTel trace context so the log record carries trace_id.
	if sc := trace.SpanFromContext(ctx).SpanContext(); sc.IsValid() {
		rec.AddAttributes(
			otellog.String("trace_id", sc.TraceID().String()),
			otellog.String("span_id", sc.SpanID().String()),
		)
	}
	logger.Emit(ctx, rec)
	return nil
}

func (b *otelBridge) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (b *otelBridge) WithAttrs(_ []slog.Attr) slog.Handler          { return b }
func (b *otelBridge) WithGroup(_ string) slog.Handler                { return b }
