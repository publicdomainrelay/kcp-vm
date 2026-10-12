package metrics

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRenderIncludesEveryFamily(t *testing.T) {
	registry := New("kcp")
	counter := registry.Counter("reconciles_total", "reconciles started")
	counter.Inc()
	counter.Add(2)
	gauge := registry.Gauge("queue_depth", "work keys waiting")
	gauge.Set(7)
	summary := registry.Summary("reconcile_seconds", "time inside a reconcile")
	summary.Observe(1.5)

	var buffer bytes.Buffer
	if err := registry.Render(&buffer); err != nil {
		t.Fatal(err)
	}
	body := buffer.String()
	for _, want := range []string{
		"# TYPE kcp_reconciles_total counter",
		"kcp_reconciles_total 3",
		"# TYPE kcp_queue_depth gauge",
		"kcp_queue_depth 7",
		"# TYPE kcp_reconcile_seconds summary",
		"kcp_reconcile_seconds_sum 1.5",
		"kcp_reconcile_seconds_count 1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered metrics must contain %q, got:\n%s", want, body)
		}
	}
}

func TestRegisteringANameTwiceReturnsTheSameCollector(t *testing.T) {
	registry := New("kcp")
	first := registry.Counter("reconciles_total", "reconciles started")
	second := registry.Counter("reconciles_total", "reconciles started")
	first.Inc()
	if second != first {
		t.Fatal("a repeated name must return the collector that is already registered")
	}
	if got := testutil.ToFloat64(second); got != 1 {
		t.Fatalf("counter = %v, want 1", got)
	}
	var buffer bytes.Buffer
	if err := registry.Render(&buffer); err != nil {
		t.Fatal(err)
	}
	if strings.Count(buffer.String(), "# TYPE kcp_reconciles_total") != 1 {
		t.Fatalf("a repeated name must be described once:\n%s", buffer.String())
	}
}

func TestRegisteringANameWithADifferentHelpPanics(t *testing.T) {
	registry := New("kcp")
	registry.Counter("reconciles_total", "reconciles started")
	defer func() {
		if recover() == nil {
			t.Fatal("the help string is part of a metric's identity; a second one must not be swallowed")
		}
	}()
	registry.Counter("reconciles_total", "a different help")
}

func TestName(t *testing.T) {
	if got := New("kcp").Name("queue_depth"); got != "kcp_queue_depth" {
		t.Fatalf("name = %q", got)
	}
	if got := New("").Name("queue_depth"); got != "queue_depth" {
		t.Fatalf("name = %q", got)
	}
}

func TestListenServesMetrics(t *testing.T) {
	registry := New("kcp")
	registry.Counter("reconciles_total", "reconciles started").Inc()
	server, err := registry.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if server.Address() == "" {
		t.Fatal("the server must report its bound address")
	}
	response, err := http.Get("http://" + server.Address() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "kcp_reconciles_total 1") {
		t.Fatalf("the endpoint must serve the metric: %s", body)
	}
}

func TestAGaugeFunctionIsReadAtScrapeAndCannotBeShared(t *testing.T) {
	registry := New("kcp")
	value := 1.0
	registry.GaugeFunc("queue_depth", "work keys waiting", func() float64 { return value })
	value = 7
	var buffer bytes.Buffer
	if err := registry.Render(&buffer); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buffer.String(), "kcp_queue_depth 7") {
		t.Fatalf("the scrape must read the function, not a captured value:\n%s", buffer.String())
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a second gauge function for the same name would export the wrong number silently")
		}
	}()
	registry.GaugeFunc("queue_depth", "work keys waiting", func() float64 { return 0 })
}

func TestAGaugeFunctionCannotStealAnotherMetricsName(t *testing.T) {
	registry := New("kcp")
	registry.Gauge("queue_depth", "work keys waiting")
	defer func() {
		if recover() == nil {
			t.Fatal("registering a function over an existing metric must not be silent")
		}
	}()
	registry.GaugeFunc("queue_depth", "work keys waiting", func() float64 { return 0 })
}

var _ prometheus.Registerer = New("kcp").Registry()

func TestAGaugeRegisteredTwiceIsShared(t *testing.T) {
	registry := New("kcp")
	first := registry.Gauge("queue_depth", "work keys waiting")
	second := registry.Gauge("queue_depth", "work keys waiting")
	if first != second {
		t.Fatal("the same gauge name and help must return the same gauge")
	}
	first.Set(3)
	if got := testutil.ToFloat64(second); got != 3 {
		t.Fatalf("gauge = %v, want 3", got)
	}
}
