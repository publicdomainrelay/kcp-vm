package gatewaykcp

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/appctl"
)

const (
	DefaultWebAppFront    = "127.0.0.1:8788"
	PortZeroWebAppFront   = "127.0.0.1:0"
	OptionGatewayAudience = "gateway-audience"
	OptionKcpPublicHost   = "kcp-public-host"
)

func ReadSpec(path string) (v1alpha1.GatewaySpec, error) {
	var s v1alpha1.GatewaySpec
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return s, fmt.Errorf("gatewaykcp: the Gateway spec file: %w", err)
	}
	if err := yaml.UnmarshalStrict(b, &s); err != nil {
		return s, fmt.Errorf("gatewaykcp: the Gateway spec file %s: %w", path, err)
	}
	return s, nil
}

func hasOption(s v1alpha1.GatewaySpec, name string) bool {
	return slices.ContainsFunc(s.Options, func(o v1alpha1.ApplicationOption) bool { return o.Name == name })
}

func Spec(s v1alpha1.GatewaySpec, profile string, environ []string) v1alpha1.GatewaySpec {
	e := newEnv(environ)
	s.Profile = profile
	n := v1alpha1.MarketNetworkValue{}
	if s.Network != nil {
		n = *s.Network
	}
	if n.PlcDirectoryUrl == "" {
		n.PlcDirectoryUrl = e.get("SOCIALWEB_GATEWAY_PLC_URL")
	}
	if n.PlcDirectoryUrl == "" {
		n.PlcDirectoryUrl = DefaultPLC
	}
	if n.RelayUrls == nil {
		n.RelayUrls = []string{}
	}
	if n.FirehoseUrls == nil {
		n.FirehoseUrls = []string{}
	}
	s.Network = &n
	if s.WebAppFront == "" {
		s.WebAppFront = PortZeroWebAppFront
		if profile == appctl.ProfileGatewayProd {
			s.WebAppFront = DefaultWebAppFront
		}
	}
	s.Options = slices.Clone(s.Options)
	if !hasOption(s, OptionGatewayAudience) {
		first, _, _ := strings.Cut(e.get("SOCIALWEB_GATEWAY_AUDIENCES"), ",")
		if first = strings.TrimSpace(first); first == "" {
			first = DefaultAudience
		}
		s.Options = append(s.Options, v1alpha1.ApplicationOption{Name: OptionGatewayAudience, Value: first})
	}
	if h := e.get("SOCIALWEB_GATEWAY_KCP_PUBLIC_HOST"); h != "" && !hasOption(s, OptionKcpPublicHost) {
		s.Options = append(s.Options, v1alpha1.ApplicationOption{Name: OptionKcpPublicHost, Value: h})
	}
	return s
}
