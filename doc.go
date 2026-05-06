// Package log provides a slog-backed implementation of [github.com/tetratelabs/telemetry]
// optimized for OpenTelemetry, with a key guarantee: metrics are always emitted even
// when the log level is silenced.
//
// # The core guarantee
//
// When you silence Info logs in production to reduce noise, paired metrics still fire.
// This is because [RecordContext] is called before the level check:
//
//	logger.SetLevel(telemetry.LevelError) // Info logs suppressed
//	logger.Metric(myMetric).Info("reserve success") // log silent, metric still increments
//
// This decouples operational verbosity from alerting signal, the right tradeoff for
// high-traffic services.
//
// # Quick start
//
//	// Wire once in main (or TestMain):
//	sink := log.NewOTelSink(meterProvider, "myservice")
//	telemetry.SetGlobalMetricSink(sink)
//	scope.UseLogger(log.New(slog.Default()))
//
//	// Declare metrics in library code (no impl dep):
//	var errs telemetry.Metric
//	func init() {
//	    telemetry.ToGlobalMetricSink(func(ms telemetry.MetricSink) {
//	        errs = ms.NewSum("myservice_errors_total", "Errors by cluster")
//	    })
//	}
//
//	// One call: log line + metric increment + OTel trace correlation:
//	logger.Context(ctx).Metric(errs).Error("reserve failed", err, "cluster", cluster)
//	// → slog: level=ERROR msg="reserve failed" trace_id=abc span_id=def cluster=openai err=...
//	// → OTel: myservice_errors_total{cluster="openai"} += 1
//
// # OTel trace correlation
//
// When a context with an active OTel span is attached via [Logger.Context], trace_id
// and span_id are automatically injected into every log line. No manual extraction needed.
//
// # Sinks
//
// [NewOTelSink]: production sink backed by a real OTel [go.opentelemetry.io/otel/sdk/metric.MeterProvider].
// Call [OTelSink.Shutdown] on exit to flush pending metrics.
//
// [NewMemSink]: in-memory sink for unit tests. Inspect recorded values with [MemSink.Snapshot].
package log
