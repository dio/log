// Command server demonstrates github.com/dio/log in a minimal HTTP server.
//
// Every request handler calls log.Metric(...).Info/Error — one call that emits
// both a structured log line and an OTel counter. Metrics are scraped at /metrics
// (Prometheus format). When the log level is raised to Error, Info logs disappear
// but the request counter keeps incrementing.
//
// Run:
//
//	go run .
//
// Then in another terminal:
//
//	curl http://localhost:8080/hello
//	curl http://localhost:8080/hello
//	curl http://localhost:8080/fail
//	curl http://localhost:9090/metrics   # see app_requests_total and app_errors_total
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	ziolog "github.com/dio/log"
	"github.com/tetratelabs/telemetry"
	"github.com/tetratelabs/telemetry/scope"

	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ---------------------------------------------------------------------------
// Metrics — declared at package level, wired to backend in main via init().
// Library code never imports the OTel SDK directly.
// ---------------------------------------------------------------------------

var (
	routeLabel telemetry.Label
	requests   telemetry.Metric
	errors_    telemetry.Metric
)

func init() {
	telemetry.ToGlobalMetricSink(func(ms telemetry.MetricSink) {
		routeLabel = ms.NewLabel("route")
		requests   = ms.NewSum("app_requests_total", "Total requests handled")
		errors_    = ms.NewSum("app_errors_total",   "Total request errors")
	})
}

var log = scope.Register("server", "HTTP server")

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func handleHello(w http.ResponseWriter, r *http.Request) {
	log.Context(r.Context()).
		Metric(requests.With(routeLabel.Upsert(r.URL.Path))).
		Info("request handled", "method", r.Method, "path", r.URL.Path)

	fmt.Fprintln(w, "hello")
}

func handleFail(w http.ResponseWriter, r *http.Request) {
	err := errors.New("something went wrong")

	log.Context(r.Context()).
		Metric(errors_.With(routeLabel.Upsert(r.URL.Path))).
		Error("request failed", err, "method", r.Method, "path", r.URL.Path)

	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sl := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Wire OTel Prometheus metrics backend.
	res, _ := resource.New(ctx, resource.WithAttributes(semconv.ServiceName("example-server")))
	promExp, err := otelprom.New()
	if err != nil {
		sl.Error("prometheus exporter", "err", err)
		os.Exit(1)
	}
	mp := metric.NewMeterProvider(metric.WithReader(promExp), metric.WithResource(res))
	defer mp.Shutdown(ctx)

	// Wire telemetry library.
	telemetry.SetGlobalMetricSink(ziolog.NewOTelSink(mp, "example"))
	scope.UseLogger(ziolog.New(sl))

	// App server.
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", handleHello)
	mux.HandleFunc("/fail",  handleFail)

	appSrv := &http.Server{Addr: ":8080", Handler: mux}

	// Admin server — /metrics only.
	adminMux := http.NewServeMux()
	adminMux.Handle("/metrics", promhttp.Handler())

	adminSrv := &http.Server{Addr: ":9090", Handler: adminMux}

	sl.Info("starting", "app", ":8080", "admin", ":9090")

	go appSrv.ListenAndServe()
	go adminSrv.ListenAndServe()

	<-ctx.Done()

	sl.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = appSrv.Shutdown(shutCtx)
	_ = adminSrv.Shutdown(shutCtx)
}
