//go:build e2e

package e2e

import (
	"context"
	"net"
	"sync"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/grpc"
)

// ---------------------------------------------------------------------------
// Sink is an in-process OTLP gRPC receiver for test assertions.
//
// Starts on a random port. Point your OTel exporters at Sink.Addr().
// Use WaitFor* helpers instead of time.Sleep; they poll at 100ms intervals.
// ---------------------------------------------------------------------------

// SpanRecord is a simplified view of a received span.
type SpanRecord struct {
	TraceID string
	SpanID  string
	Name    string
	Attrs   map[string]string
}

// LogRecord is a simplified view of a received log record.
type LogRecord struct {
	Body    string
	TraceID string
	Attrs   map[string]string
}

// MetricPoint is one data point from a received metric.
type MetricPoint struct {
	Name   string
	Labels map[string]string
	Value  int64 // int counter; use float64 field for histograms if needed
}

// Sink is an in-process OTLP gRPC server that collects telemetry for assertions.
type Sink struct {
	mu      sync.Mutex
	spans   []SpanRecord
	logs    []LogRecord
	metrics []MetricPoint

	server *grpc.Server
	addr   string
}

// NewSink starts the sink on a random port and returns it.
func NewSink() (*Sink, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Sink{addr: ln.Addr().String()}
	s.server = grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(s.server, &traceServiceImpl{sink: s})
	collogspb.RegisterLogsServiceServer(s.server, &logsServiceImpl{sink: s})
	colmetricspb.RegisterMetricsServiceServer(s.server, &metricsServiceImpl{sink: s})
	go s.server.Serve(ln)
	return s, nil
}

// Addr returns the "host:port" the sink is listening on.
func (s *Sink) Addr() string { return s.addr }

// Stop shuts down the gRPC server.
func (s *Sink) Stop() { s.server.GracefulStop() }

// Reset clears all collected data between tests.
func (s *Sink) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spans = nil
	s.logs = nil
	s.metrics = nil
}

// ---------------------------------------------------------------------------
// WaitFor* helpers poll until predicate matches or timeout expires.
// ---------------------------------------------------------------------------

// WaitForCounter waits until the named counter (with optional label match) has
// accumulated at least minVal across all received data points.
func (s *Sink) WaitForCounter(name, labelKey, labelVal string, minVal int64, timeout time.Duration) (int64, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if total := s.sumCounter(name, labelKey, labelVal); total >= minVal {
			return total, true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return s.sumCounter(name, labelKey, labelVal), false
}

// WaitForLog waits until a log record whose Body contains bodyContains is received.
func (s *Sink) WaitForLog(bodyContains string, timeout time.Duration) (LogRecord, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, l := range s.logs {
			if contains(l.Body, bodyContains) {
				s.mu.Unlock()
				return l, true
			}
		}
		s.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}
	return LogRecord{}, false
}

// WaitForSpan waits until a span with the given traceID is received.
func (s *Sink) WaitForSpan(traceID string, timeout time.Duration) (SpanRecord, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, sp := range s.spans {
			if sp.TraceID == traceID {
				s.mu.Unlock()
				return sp, true
			}
		}
		s.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}
	return SpanRecord{}, false
}

func (s *Sink) sumCounter(name, labelKey, labelVal string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	for _, p := range s.metrics {
		if p.Name != name {
			continue
		}
		if labelKey != "" && p.Labels[labelKey] != labelVal {
			continue
		}
		total += p.Value
	}
	return total
}

// ---------------------------------------------------------------------------
// gRPC service implementations, one wrapper per service to satisfy interface.
// ---------------------------------------------------------------------------

type traceServiceImpl struct {
	coltracepb.UnimplementedTraceServiceServer
	sink *Sink
}

func (t *traceServiceImpl) Export(_ context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	t.sink.mu.Lock()
	defer t.sink.mu.Unlock()
	for _, rs := range req.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				t.sink.spans = append(t.sink.spans, SpanRecord{
					TraceID: hexID(sp.TraceId),
					SpanID:  hexID(sp.SpanId),
					Name:    sp.Name,
					Attrs:   kvToMap(sp.Attributes),
				})
			}
		}
	}
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

type logsServiceImpl struct {
	collogspb.UnimplementedLogsServiceServer
	sink *Sink
}

func (l *logsServiceImpl) Export(_ context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	l.sink.mu.Lock()
	defer l.sink.mu.Unlock()
	for _, rl := range req.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				l.sink.logs = append(l.sink.logs, LogRecord{
					Body:    lr.Body.GetStringValue(),
					TraceID: hexID(lr.TraceId),
					Attrs:   kvToMap(lr.Attributes),
				})
			}
		}
	}
	return &collogspb.ExportLogsServiceResponse{}, nil
}

type metricsServiceImpl struct {
	colmetricspb.UnimplementedMetricsServiceServer
	sink *Sink
}

func (m *metricsServiceImpl) Export(_ context.Context, req *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	m.sink.mu.Lock()
	defer m.sink.mu.Unlock()
	for _, rm := range req.ResourceMetrics {
		for _, sm := range rm.ScopeMetrics {
			for _, metric := range sm.Metrics {
				m.sink.metrics = append(m.sink.metrics, extractPoints(metric)...)
			}
		}
	}
	return &colmetricspb.ExportMetricsServiceResponse{}, nil
}

// extractPoints pulls data points from a Metric regardless of type.
func extractPoints(m *metricspb.Metric) []MetricPoint {
	var pts []MetricPoint
	switch d := m.Data.(type) {
	case *metricspb.Metric_Sum:
		for _, dp := range d.Sum.DataPoints {
			pts = append(pts, MetricPoint{
				Name:   m.Name,
				Labels: kvToMap(dp.Attributes),
				Value:  dp.GetAsInt(),
			})
		}
	case *metricspb.Metric_Gauge:
		for _, dp := range d.Gauge.DataPoints {
			pts = append(pts, MetricPoint{
				Name:   m.Name,
				Labels: kvToMap(dp.Attributes),
				Value:  dp.GetAsInt(),
			})
		}
	}
	return pts
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func kvToMap(kvs []*commonpb.KeyValue) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		m[kv.Key] = kv.Value.GetStringValue()
	}
	return m
}

func hexID(b []byte) string {
	const hextable = "0123456789abcdef"
	dst := make([]byte, len(b)*2)
	for i, v := range b {
		dst[i*2] = hextable[v>>4]
		dst[i*2+1] = hextable[v&0x0f]
	}
	return string(dst)
}

func contains(s, sub string) bool {
	return len(sub) == 0 || len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
