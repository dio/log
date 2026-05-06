//go:build e2e

// Package e2e tests the github.com/dio/log library against a real otel-front sink.
//
// Run:
//
//	cd e2e && go test -v -tags e2e -timeout 60s ./...
//
// otel-front is started automatically via Docker and torn down after the suite.
// Set E2E_SKIP_DOCKER=1 to reuse an already-running instance on the default ports.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
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

	ziolog "github.com/dio/log"
)

const (
	otlpGRPCAddr = "localhost:4317"
	apiAddr      = "http://localhost:8000"
	containerName = "otel-front-e2e"
)

// ---------------------------------------------------------------------------
// Metric declarations (same pattern as quotasvc)
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
// TestMain — start otel-front, wire OTel SDK, run suite, teardown.
// ---------------------------------------------------------------------------

var tracer trace.Tracer
var tp    *sdktrace.TracerProvider

func TestMain(m *testing.M) {
	ctx := context.Background()

	if os.Getenv("E2E_SKIP_DOCKER") == "" {
		startOtelFront()
	}
	waitReady(apiAddr+"/health", 20*time.Second)

	// OTel resource
	res, _ := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName("zia-log-e2e")),
	)

	// Trace exporter → otel-front gRPC
	traceExp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(otlpGRPCAddr),
		otlptracegrpc.WithInsecure(),
	)
	must("trace exporter", err)
	tp = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	tracer = tp.Tracer("zia-e2e")

	// Metric exporter → otel-front gRPC
	metricExp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(otlpGRPCAddr),
		otlpmetricgrpc.WithInsecure(),
	)
	must("metric exporter", err)
	mp := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExp,
			metric.WithInterval(500*time.Millisecond))), // fast flush for tests
		metric.WithResource(res),
	)

	// Log exporter → otel-front gRPC (OTLP Logs)
	logExp, err := otlploggrpc.New(ctx,
		otlploggrpc.WithEndpoint(otlpGRPCAddr),
		otlploggrpc.WithInsecure(),
	)
	must("log exporter", err)
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)

	// Wire telemetry library
	sink := ziolog.NewOTelSink(mp, "zia")
	telemetry.SetGlobalMetricSink(sink)

	// slog → OTel log bridge so our log lines also go to otel-front
	otelBridge := &otelLogRecord{provider: lp}
	sl := slog.New(slog.NewJSONHandler(os.Stderr, nil)) // stderr for local visibility
	_ = sl
	scope.UseLogger(ziolog.New(slog.New(otelBridge)))

	code := m.Run()

	// Flush before exit
	_ = tp.ForceFlush(ctx)
	_ = mp.ForceFlush(ctx)
	_ = lp.ForceFlush(ctx)
	_ = tp.Shutdown(ctx)
	_ = mp.Shutdown(ctx)
	_ = lp.Shutdown(ctx)

	if os.Getenv("E2E_SKIP_DOCKER") == "" {
		stopOtelFront()
	}

	os.Exit(code)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestLogAndMetricReachOtelFront is the end-to-end proof:
// one log.Metric(...).Info(...) call → metric visible in otel-front API.
func TestLogAndMetricReachOtelFront(t *testing.T) {
	ctx, span := tracer.Start(context.Background(), "quota.Reserve")
	traceID := span.SpanContext().TraceID().String()

	// Single call: log + metric + trace correlation
	quotaLog.Context(ctx).
		Metric(reserveOK.With(clusterLabel.Upsert("openai"))).
		Info("reserve success", "user_id", "alice", "tokens", 1000)

	quotaLog.Context(ctx).
		Metric(reserveErrors.With(clusterLabel.Upsert("anthropic"))).
		Error("reserve failed", context.DeadlineExceeded, "user_id", "bob")

	// End span explicitly then flush so otel-front receives it before we assert.
	span.End()
	_ = tp.ForceFlush(ctx)

	// Give periodic reader time to flush metrics too.
	time.Sleep(2 * time.Second)

	assertMetricExists(t, "zia_quota_reserve_total")
	assertMetricExists(t, "zia_quota_reserve_errors_total")
	assertTraceExists(t, traceID)

	t.Logf("trace_id=%s visible in otel-front at %s/traces", traceID, apiAddr)
}

// TestMetricFiresWhenLogSilenced — even with Error-only log level, metric fires.
func TestMetricFiresWhenLogSilenced(t *testing.T) {
	var silenced telemetry.Metric
	telemetry.ToGlobalMetricSink(func(ms telemetry.MetricSink) {
		silenced = ms.NewSum("zia_silenced_events_total", "Silenced info events")
	})

	sl := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	logger := ziolog.New(sl)
	logger.SetLevel(telemetry.LevelError)

	logger.Metric(silenced).Info("this log is dropped") // ← silent, metric still fires

	time.Sleep(2 * time.Second)
	assertMetricExists(t, "zia_silenced_events_total")
}

// ---------------------------------------------------------------------------
// REST API assertions
// ---------------------------------------------------------------------------

func assertMetricExists(t *testing.T, name string) {
	t.Helper()
	url := fmt.Sprintf("%s/api/metrics/names", apiAddr)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	var result struct {
		Names []string `json:"names"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode metric names: %v", err)
	}

	for _, n := range result.Names {
		if n == name {
			t.Logf("metric %q found in otel-front", name)
			return
		}
	}
	t.Errorf("metric %q not found in otel-front; got: %v", name, result.Names)
}

func assertTraceExists(t *testing.T, traceID string) {
	t.Helper()
	url := fmt.Sprintf("%s/api/traces/%s", apiAddr, traceID)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Logf("trace %s found in otel-front", traceID)
		return
	}
	t.Errorf("trace %s not found, status=%d", traceID, resp.StatusCode)
}

// ---------------------------------------------------------------------------
// Docker lifecycle
// ---------------------------------------------------------------------------

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

func waitReady(url string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	// Also try raw TCP in case health endpoint is slow
	if _, err := net.DialTimeout("tcp", otlpGRPCAddr, 2*time.Second); err == nil {
		return
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

// otelLogRecord bridges slog → OTel log SDK (so log lines reach otel-front).
type otelLogRecord struct {
	provider *sdklog.LoggerProvider
}

func (b *otelLogRecord) Handle(ctx context.Context, r slog.Record) error {
	logger := b.provider.Logger("zia-log")
	var rec otellog.Record
	rec.SetTimestamp(r.Time)
	rec.SetSeverityText(r.Level.String())
	rec.SetBody(otellog.StringValue(r.Message))
	r.Attrs(func(a slog.Attr) bool {
		rec.AddAttributes(otellog.String(a.Key, fmt.Sprint(a.Value.Any())))
		return true
	})
	logger.Emit(ctx, rec)
	return nil
}

func (b *otelLogRecord) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (b *otelLogRecord) WithAttrs(attrs []slog.Attr) slog.Handler    { return b }
func (b *otelLogRecord) WithGroup(name string) slog.Handler           { return b }
