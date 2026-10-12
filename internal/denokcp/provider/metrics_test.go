package provider

import (
	"bytes"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

func renderMetrics(t *testing.T, p *Provider) string {
	t.Helper()
	var buffer bytes.Buffer
	if err := p.metrics.registry.Render(&buffer); err != nil {
		t.Fatal(err)
	}
	return buffer.String()
}

func metricValue(t *testing.T, body, name string) float64 {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		if rest, ok := strings.CutPrefix(line, name+" "); ok {
			value, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
			if err != nil {
				t.Fatalf("metric %s value %q: %v", name, rest, err)
			}
			return value
		}
	}
	t.Fatalf("metric %s not found in:\n%s", name, body)
	return 0
}

func TestMetricsExposition(t *testing.T) {
	p := &Provider{}
	p.initMetrics()
	p.metrics.reconciles.Add(3)
	p.metrics.conflicts.Add(1)
	p.recordReconcile(10*time.Millisecond, nil)
	p.recordReconcile(30*time.Millisecond, errors.New("conflict"))
	body := renderMetrics(t, p)

	if got := metricValue(t, body, "denokcp_reconciles_total"); got != 3 {
		t.Fatalf("reconciles = %v, want 3", got)
	}
	if got := metricValue(t, body, "denokcp_conflicts_total"); got != 1 {
		t.Fatalf("conflicts = %v, want 1", got)
	}
	if got := metricValue(t, body, "denokcp_errors_total"); got != 1 {
		t.Fatalf("errors = %v, want 1", got)
	}
	if got := metricValue(t, body, "denokcp_queue_depth"); got != 0 {
		t.Fatalf("queue depth = %v, want 0", got)
	}
	if got := metricValue(t, body, "denokcp_reconcile_seconds_count"); got != 2 {
		t.Fatalf("reconcile seconds count = %v, want 2", got)
	}
	if got := metricValue(t, body, "denokcp_reconcile_seconds_sum"); math.Abs(got-0.04) > 1e-9 {
		t.Fatalf("reconcile seconds sum = %v, want 0.04", got)
	}
	if got := metricValue(t, body, "denokcp_active_runs"); got != 0 {
		t.Fatalf("active runs = %v, want 0", got)
	}
	for _, want := range []string{
		"# TYPE denokcp_queue_depth gauge",
		"# TYPE denokcp_reconciles_total gauge",
		"# TYPE denokcp_reconcile_seconds summary",
		"# TYPE denokcp_active_runs gauge",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics output missing %q:\n%s", want, body)
		}
	}
}

func TestCacheAgeIsNaNBeforeEvents(t *testing.T) {
	p := &Provider{}
	p.initMetrics()
	if got := metricValue(t, renderMetrics(t, p), "denokcp_cache_age_seconds"); !math.IsNaN(got) {
		t.Fatalf("cache age = %v, want NaN before any event", got)
	}
	p.recordEvent()
	if age := metricValue(t, renderMetrics(t, p), "denokcp_cache_age_seconds"); age < 0 || age > 5 {
		t.Fatalf("cache age = %v, want a small non-negative value", age)
	}
}
