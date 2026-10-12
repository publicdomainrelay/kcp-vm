package provider

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/pki"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/denospec"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/factory/servicenames"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/assets"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/pkiprovisioner"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

func (p *Provider) buildServices() *servicenames.Resolver {
	var paths servicenames.PathResolver
	if p.paths != nil {
		paths = p.paths
	}
	return servicenames.New(servicenames.Options{
		Source:                  servicenames.SourceFunc(p.allPods),
		Minter:                  p.opts.Minter,
		Paths:                   paths,
		Domain:                  p.opts.ServiceDomain,
		TokenTTL:                p.opts.TokenTTL,
		ServiceAccountNamespace: DefaultServiceAccountNamespace,
		Shim:                    p.dnsShim,
		TableFile:               p.dnsTable,
	})
}

func (p *Provider) serviceResolver() *servicenames.Resolver {
	p.servicesOnce.Do(func() {
		if p.services == nil {
			p.services = p.buildServices()
		}
	})
	return p.services
}

func (p *Provider) podEnv(ctx context.Context, ref Ref, tmpl *v1alpha1.DenoPodTemplate) map[string]string {
	env := map[string]string{}
	for k, v := range tmpl.Env {
		env[k] = v
	}
	services := p.serviceResolver()
	for k, v := range services.Env(ctx, ref, tmpl.Env["SERVICE_ARGS"], tmpl.Env["SERVICE_ENV"], tmpl.ServiceAccount.DenoSpec()) {
		env[k] = v
	}
	// ponytail: the runner writes the trust bundle to ca.pem in the run
	// directory, which is this process's working directory, so DENO_CERT is a
	// relative path on purpose. Deno reads it at startup, which is the only
	// moment it can be set: a workload that does its own TLS cannot install a CA
	// from inside the script.
	if len(p.clusterCA) > 0 {
		env["DENO_CERT"] = "ca.pem"
	}
	// ponytail: TLS is opt-in per pod, because a workload that does not serve
	// anything has no use for a certificate and no reason to carry one. The leaf
	// is signed for the pod's own name by the intermediate of the namespace the
	// pod is in -- which is why a namespace needs an OpenBao object before any of
	// its workloads can serve TLS -- and carries loopback as a subject alternative
	// name, which is what a probe reaches it on and what the shim's rewritten
	// address verifies against.
	if p.pki != nil && tmpl.Env["SERVICE_TLS"] == "true" {
		fqdn := services.Name(ctx, ref.Name, ref.Namespace, ref.LogicalCluster)
		cert, err := p.issueFor(ctx, ref, fqdn)
		if err != nil {
			p.opts.Log.Warn("openbao: no certificate for this workload, it will not serve TLS", "name", fqdn, "err", err)
		} else {
			env["KCP_TLS_CERT"] = pkiprovisioner.LeafChain(cert)
			env["KCP_TLS_KEY"] = cert.PrivateKey
			env["KCP_SERVICE_NAME"] = fqdn
		}

		env["KCP_CA_BUNDLE"] = string(p.pki.CachedRootPEM()) + string(p.clusterCA)
	}
	return env
}

func (p *Provider) probeCommand(command []string) []string {
	if len(command) < 2 || command[0] != "kcpdns" {
		return command
	}
	if p.dnsShim == "" || p.dnsProbe == "" {
		return nil
	}
	var reads []string
	if p.dnsTable != "" {
		reads = []string{p.dnsTable}
	}
	return denospec.ProbeCommandReading("", p.dnsShim, p.dnsProbe, command[1], probePath(command), assets.DNSProbeEnv, reads)
}

func probePath(command []string) string {
	if len(command) > 2 && command[2] != "" {
		return command[2]
	}
	return "/"
}

func (p *Provider) publishAddress(ctx context.Context, ref Ref, address string) {
	services := p.serviceResolver()
	services.Observe(ctx, ref, address)
	if _, err := services.WriteTableFile(ctx); err != nil {
		p.opts.Log.Warn("kcpdns: the table file was not rewritten, peers keep the addresses they had", "err", err)
	}
}

func (p *Provider) allPods() []*unstructured.Unstructured {
	if lister, ok := p.reader.(podLister); ok {
		return lister.allPods()
	}
	return nil
}

type podLister interface {
	allPods() []*unstructured.Unstructured
}

func (p *Provider) issueFor(ctx context.Context, ref Ref, fqdn string) (pki.Cert, error) {
	obj, err := p.authorityFor(ctx, ref.LogicalCluster, ref.Namespace)
	if err != nil {
		return pki.Cert{}, err
	}
	return p.pki.Issue(ctx, obj.Spec.Namespace, fqdn, []string{fqdn}, nil)
}
