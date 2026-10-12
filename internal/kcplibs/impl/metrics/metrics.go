package metrics

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
)

type Registry struct {
	registry *prometheus.Registry

	prefix string
}

func New(prefix string) *Registry {
	return &Registry{registry: prometheus.NewRegistry(), prefix: prefix}
}

func (r *Registry) Registry() *prometheus.Registry {
	return r.registry
}

func (r *Registry) format() expfmt.Format {
	return expfmt.NewFormat(expfmt.TypeTextPlain)
}

func (r *Registry) Name(name string) string {
	if r.prefix == "" {
		return name
	}
	return r.prefix + "_" + name
}

func (r *Registry) Counter(name, help string) prometheus.Counter {
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: r.Name(name), Help: help})
	return existing[prometheus.Counter](r, counter)
}

func (r *Registry) Gauge(name, help string) prometheus.Gauge {
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: r.Name(name), Help: help})
	return existing[prometheus.Gauge](r, gauge)
}

func (r *Registry) GaugeFunc(name, help string, value func() float64) prometheus.GaugeFunc {
	gauge := prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: r.Name(name), Help: help}, value)
	if err := r.registry.Register(gauge); err != nil {
		panic(fmt.Sprintf("metrics: %v; a gauge function is bound to one caller, so two of them cannot share a name", err))
	}
	return gauge
}

func (r *Registry) Summary(name, help string) prometheus.Summary {
	summary := prometheus.NewSummary(prometheus.SummaryOpts{Name: r.Name(name), Help: help})
	return existing[prometheus.Summary](r, summary)
}

func existing[T prometheus.Collector](r *Registry, collector T) T {
	if err := r.registry.Register(collector); err != nil {
		var registered prometheus.AlreadyRegisteredError
		if errors.As(err, &registered) {
			if already, ok := registered.ExistingCollector.(T); ok {
				return already
			}
		}
		panic(fmt.Sprintf("metrics: %v", err))
	}
	return collector
}

func (r *Registry) Render(w io.Writer) error {
	families, err := r.registry.Gather()
	if err != nil {
		return fmt.Errorf("metrics: gather: %w", err)
	}
	encoder := expfmt.NewEncoder(w, r.format())
	for _, family := range families {
		if err := encoder.Encode(family); err != nil {
			return fmt.Errorf("metrics: encode %s: %w", family.GetName(), err)
		}
	}
	return nil
}

func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}

type Server struct {
	listener net.Listener

	server *http.Server
}

func (r *Registry) Listen(address string) (*Server, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("metrics: bind %s: %w", address, err)
	}
	server := &http.Server{Handler: r.Handler()}
	go func() {
		_ = server.Serve(listener)
	}()
	return &Server{listener: listener, server: server}, nil
}

func (s *Server) Address() string {
	if s == nil || s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Close()
}
