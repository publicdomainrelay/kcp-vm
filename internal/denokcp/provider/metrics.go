package provider

import (
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/metrics"
)

const metricsPrefix = "denokcp"

type providerMetrics struct {
	registry *metrics.Registry

	reconciles atomic.Uint64

	conflicts atomic.Uint64

	errors atomic.Uint64

	reconcileSeconds summaryObserver

	lastEventNanos atomic.Int64
}

type summaryObserver interface {
	Observe(float64)
}

// ponytail: reconciles, conflicts and errors are gauges read from the provider's
// own atomics, not prometheus counters. The provider increments the atomics and
// keeps them as its state; a counter cannot be seeded from a value the provider
// already tracks, and Provider.Reconciles() reads the atomic, so a counter here
// would be a second, unreadable copy. The names keep their _total suffix.
func (p *Provider) initMetrics() {
	registry := metrics.New(metricsPrefix)
	registry.GaugeFunc("queue_depth", "work keys waiting in the reconcile queue", func() float64 {
		return float64(p.queueDepth())
	})
	registry.GaugeFunc("cache_age_seconds", "seconds since the last informer event, NaN before the first one", func() float64 {
		return p.cacheAgeSeconds()
	})
	registry.GaugeFunc("reconciles_total", "reconciles started", func() float64 {
		return float64(p.metrics.reconciles.Load())
	})
	registry.GaugeFunc("conflicts_total", "writes rejected by optimistic concurrency", func() float64 {
		return float64(p.metrics.conflicts.Load())
	})
	registry.GaugeFunc("errors_total", "reconciles that returned an error", func() float64 {
		return float64(p.metrics.errors.Load())
	})
	p.metrics.reconcileSeconds = registry.Summary("reconcile_seconds", "time spent inside a reconcile")
	registry.GaugeFunc("active_runs", "workloads started and not yet terminal", func() float64 {
		return float64(p.ActiveRuns())
	})
	registry.GaugeFunc("max_active_runs", "high-water mark of denokcp_active_runs", func() float64 {
		return float64(p.MaxActiveRuns())
	})
	p.metrics.registry = registry
}

func (p *Provider) recordReconcile(d time.Duration, err error) {
	if p.metrics.reconcileSeconds != nil {
		p.metrics.reconcileSeconds.Observe(d.Seconds())
	}
	if err != nil {
		p.metrics.errors.Add(1)
	}
}

func (p *Provider) recordConflict() {
	p.metrics.conflicts.Add(1)
}

func (p *Provider) recordEvent() {
	p.metrics.lastEventNanos.Store(time.Now().UnixNano())
}

func (p *Provider) queueDepth() int64 {
	if p.watch == nil {
		return 0
	}
	return int64(p.watch.queue.Len())
}

// ponytail: before the first informer event the age is unknown, so this reports
// NaN rather than a zero that would claim the cache was just refreshed. The
// library's controller reports NaN for the same reason.
func (p *Provider) cacheAgeSeconds() float64 {
	last := p.metrics.lastEventNanos.Load()
	if last == 0 {
		return math.NaN()
	}
	return time.Since(time.Unix(0, last)).Seconds()
}

func (p *Provider) serveMetrics(listen string) error {
	server, err := p.metrics.registry.Listen(listen)
	if err != nil {
		return fmt.Errorf("provider: bind metrics endpoint on %s: %w", listen, err)
	}
	p.metricsSrv = server
	return nil
}
