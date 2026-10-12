package clientlimit

import "k8s.io/client-go/rest"

const DefaultQPS float32 = 50

const DefaultBurst = 100

func Apply(cfg *rest.Config, qps float32, burst int) *rest.Config {
	out := rest.CopyConfig(cfg)
	if qps <= 0 {
		qps = DefaultQPS
	}
	if burst <= 0 {
		burst = DefaultBurst
	}
	if out.QPS <= 0 {
		out.QPS = qps
	}
	if out.Burst <= 0 {
		out.Burst = burst
	}
	return out
}
