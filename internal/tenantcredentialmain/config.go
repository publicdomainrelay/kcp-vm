package tenantcredentialmain

import (
	"fmt"
	"strings"
	"time"
)

const (
	envStateDir = "SOCIALWEB_TENANTCREDENTIAL_STATE_DIR"

	envCluster = "SOCIALWEB_TENANTCREDENTIAL_CLUSTER"

	envServer = "SOCIALWEB_TENANTCREDENTIAL_SERVER"

	envCommonName = "SOCIALWEB_TENANTCREDENTIAL_COMMON_NAME"

	envOrganizations = "SOCIALWEB_TENANTCREDENTIAL_ORGANIZATIONS"

	envOutDir = "SOCIALWEB_TENANTCREDENTIAL_OUT_DIR"

	envValidFor = "SOCIALWEB_TENANTCREDENTIAL_VALID_FOR"
)

type Config struct {
	StateDir string

	Cluster string

	Server string

	CommonName string

	Organizations []string

	OutDir string

	ValidFor time.Duration
}

func load(get func(string) string) (Config, error) {
	cfg := Config{
		StateDir:   strings.TrimSpace(get(envStateDir)),
		Cluster:    strings.TrimSpace(get(envCluster)),
		Server:     strings.TrimSpace(get(envServer)),
		CommonName: strings.TrimSpace(get(envCommonName)),
		OutDir:     strings.TrimSpace(get(envOutDir)),
	}

	for _, req := range []struct{ name, value, why string }{
		{envStateDir, cfg.StateDir,
			"it is the clusterprovider's state dir, where each cluster's data dir lives, and the " +
				"cluster's own ClientCA -- the only thing a tenant credential can be signed with -- " +
				"is under it. With no state dir there is no CA to sign with, and a run that guessed " +
				"one would sign against whatever CA happened to sit in the working directory"},
		{envCluster, cfg.Cluster,
			"it names which data dir under the state dir to sign from, because a host can hold " +
				"several clusters and each has its own ClientCA. An unset cluster is a credential " +
				"minted from no cluster, or from whichever one a default picked, which is a " +
				"credential for a cluster the operator did not name"},
		{envServer, cfg.Server,
			"it is the https URL of the tenant apiserver this credential authenticates to, and it " +
				"is also the host name the apiserver's own certificate must carry. With no URL there " +
				"is no server identity to check, and the credential would verify nothing"},
		{envCommonName, cfg.CommonName,
			"it is the username the apiserver reads off the certificate. An unset one cannot be " +
				"compared against what was minted, and that comparison is the only thing that catches " +
				"a credential whose identity is not the one the operator's RBAC binds"},
		{envOutDir, cfg.OutDir,
			"it is where ca.crt, tls.crt and tls.key are written, and there is no default for it: a " +
				"credential written to a guessed directory is one the operator cannot find and cannot " +
				"delete"},
	} {
		if req.value == "" {
			return Config{}, fmt.Errorf("%s is not set. %s", req.name, req.why)
		}
	}

	raw := strings.TrimSpace(get(envOrganizations))
	if raw == "" {
		return Config{}, fmt.Errorf(
			"%s is not set. The apiserver reads the groups off the certificate's Organizations, so "+
				"a credential with no organization carries no group and authenticates as an "+
				"unprivileged user: every object the tenant grant exists to apply would be refused, "+
				"and the refusal reads as a broken grant rather than as a credential that was asked "+
				"for without one", envOrganizations)
	}
	parts := strings.Split(raw, ",")
	organizations := make([]string, 0, len(parts))
	for _, part := range parts {
		organizations = append(organizations, strings.TrimSpace(part))
	}
	cfg.Organizations = organizations

	rawValid := strings.TrimSpace(get(envValidFor))
	if rawValid != "" {
		d, err := time.ParseDuration(rawValid)
		if err != nil {
			return Config{}, fmt.Errorf("%s is %q and it is not a Go duration such as 720h: %w",
				envValidFor, rawValid, err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf(
				"%s is %q. A non-positive validity is not honoured by the minting path, which "+
					"substitutes its own default, so an operator who asked for it would believe they "+
					"had it: the duration is refused rather than silently replaced",
				envValidFor, rawValid)
		}
		cfg.ValidFor = d
	}
	return cfg, nil
}
