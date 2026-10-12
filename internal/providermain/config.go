package providermain

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/eso"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostprocess"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

const (
	envKCPHost       = "SOCIALWEB_KCP_HOST"
	envKCPKubeconfig = "SOCIALWEB_KCP_KUBECONFIG"

	envMountContext = "SOCIALWEB_CLUSTERPROVIDER_MOUNT_CONTEXT"

	envMintKubeconfigs = "SOCIALWEB_GATEWAY_MINT_KUBECONFIGS"

	envGrant = "SOCIALWEB_CLUSTERPROVIDER_GRANT"

	envLeasePath = "SOCIALWEB_CLUSTERPROVIDER_LEASE_PATH"

	envPortRange = "SOCIALWEB_CLUSTERPROVIDER_PORT_RANGE"

	envJoin = "SOCIALWEB_CLUSTERPROVIDER_JOIN"

	envDatastore = "SOCIALWEB_CLUSTERPROVIDER_DATASTORE"

	envStateDir = "SOCIALWEB_CLUSTERPROVIDER_STATE_DIR"

	envUnitPrefix           = "SOCIALWEB_CLUSTERPROVIDER_UNIT_PREFIX"
	envHostProcessWorkspace = "SOCIALWEB_CLUSTERPROVIDER_HOSTPROCESS_WORKSPACE"
	envDatastoreHasRow      = "SOCIALWEB_CLUSTERPROVIDER_DATASTORE_HAS_ROW"

	envAdvertiseAddress = "SOCIALWEB_CLUSTERPROVIDER_ADVERTISE_ADDRESS"
	envStoreEndpoint    = "SOCIALWEB_CLUSTERPROVIDER_STORE_ENDPOINT"
	envAuditPolicy      = "SOCIALWEB_CLUSTERPROVIDER_AUDIT_POLICY"

	envMarketAdvertiseAddress = "SOCIALWEB_CLUSTERPROVIDER_MARKET_ADVERTISE_ADDRESS"
	envMarketDenyBidderDids   = "SOCIALWEB_CLUSTERPROVIDER_MARKET_DENY_BIDDER_DIDS"
	envMarketReleaseTimeout   = "SOCIALWEB_CLUSTERPROVIDER_MARKET_RELEASE_TIMEOUT"

	envScaleHold = "SOCIALWEB_CLUSTERPROVIDER_SCALE_HOLD"

	envNodeJoinGrace = "SOCIALWEB_CLUSTERPROVIDER_NODE_JOIN_GRACE"

	envScaleMaxNodes = "SOCIALWEB_CLUSTERPROVIDER_SCALE_MAX_NODES"

	envScalePodsPerNode = "SOCIALWEB_CLUSTERPROVIDER_SCALE_PODS_PER_NODE"

	envSubstrate = "SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE"
	envHost      = "SOCIALWEB_CLUSTERPROVIDER_HOST"

	envRetiredSubstrateInput = "SOCIALWEB_CLUSTERPROVIDER_"

	envCloudControllerManager = "SOCIALWEB_CLUSTERPROVIDER_CLOUD_CONTROLLER_MANAGER"

	envDumbpipe = "SOCIALWEB_CLUSTERPROVIDER_DUMBPIPE"

	envTokenTimeout = "SOCIALWEB_CLUSTERPROVIDER_TOKEN_TIMEOUT"
	envLogLevel     = "SOCIALWEB_CLUSTERPROVIDER_LOG_LEVEL"

	envESOManifestDir = "SOCIALWEB_CLUSTERPROVIDER_ESO_MANIFEST_DIR"

	envESONamespace = "SOCIALWEB_CLUSTERPROVIDER_ESO_NAMESPACE"

	envExternalSecrets = "SOCIALWEB_CLUSTERPROVIDER_EXTERNAL_SECRETS"

	envESOBundle = "SOCIALWEB_CLUSTERPROVIDER_ESO_BUNDLE"
)

const DefaultESOManifestDir = "/opt/socialweb/deploy/eso"

const SubstrateMarket = "market"

const RetiredSubstratesRetiredOn = "2026-10-07"

var retiredSubstrateNames = []string{"firecracker", "qemu"}

var retiredSubstrateInputs = []string{
	envRetiredSubstrateInput + "FIRECRACKER",
	envRetiredSubstrateInput + "QEMU",
	envRetiredSubstrateInput + "NODE_AGENT",
	envRetiredSubstrateInput + "IMAGE_OUT_DIR",
	envRetiredSubstrateInput + "IMAGE_CONFIG",
	envRetiredSubstrateInput + "NETWORK",
	envRetiredSubstrateInput + "NODE_DISK_MIB",
}

const DefaultGrantPath = "deploy/tenant-grant.yaml"

const DefaultUnitPrefix = "socialweb-"

const DefaultInterval = 2 * time.Second

const DefaultReclaimGrace = 5 * time.Minute

type Config struct {
	KCPHost string

	KCPKubeconfig string

	MountContext string

	MintKubeconfigs string

	GrantPath string

	LeasePath string

	PortRange portallocator.Range

	Join string

	Datastore string

	StateDir string

	UnitPrefix string

	HostProcessWorkspace string

	AdvertiseAddress string

	MarketAdvertiseAddress string

	MarketDenyBidderDids []string

	MarketReleaseTimeout time.Duration

	StoreEndpoint string

	AuditPolicyPath string

	Substrate string

	Host string

	CloudControllerManager string

	CloudControllerManagerExec []string

	FrontExec []string

	HostnameExec []string

	Dumbpipe string

	ESOManifestDir string

	ESONamespace string

	ExternalSecrets string

	ESOBundle string

	DatastoreHasRow bool

	ScaleHold time.Duration

	NodeJoinGrace time.Duration

	ScaleMaxNodes int

	ScalePodsPerNode int

	TokenTimeout time.Duration

	Interval time.Duration

	LogLevel slog.Level
}

func load(get func(string) string) (Config, error) {
	return loadFor(get, foldmain.Invocation{})
}

func loadFor(get func(string) string, inv foldmain.Invocation) (Config, error) {
	cfg := Config{
		KCPHost:                strings.TrimSpace(get(envKCPHost)),
		KCPKubeconfig:          strings.TrimSpace(get(envKCPKubeconfig)),
		MountContext:           strings.TrimSpace(get(envMountContext)),
		MintKubeconfigs:        strings.TrimSpace(get(envMintKubeconfigs)),
		LeasePath:              strings.TrimSpace(get(envLeasePath)),
		Join:                   strings.TrimSpace(get(envJoin)),
		Datastore:              strings.TrimSpace(get(envDatastore)),
		StateDir:               strings.TrimSpace(get(envStateDir)),
		UnitPrefix:             strings.TrimSpace(get(envUnitPrefix)),
		HostProcessWorkspace:   strings.TrimSpace(get(envHostProcessWorkspace)),
		AdvertiseAddress:       strings.TrimSpace(get(envAdvertiseAddress)),
		MarketAdvertiseAddress: strings.TrimSpace(get(envMarketAdvertiseAddress)),
		StoreEndpoint:          strings.TrimSpace(get(envStoreEndpoint)),
		AuditPolicyPath:        strings.TrimSpace(get(envAuditPolicy)),
		Substrate:              strings.TrimSpace(get(envSubstrate)),
		Host:                   strings.TrimSpace(get(envHost)),
		CloudControllerManager: strings.TrimSpace(get(envCloudControllerManager)),
		Dumbpipe:               strings.TrimSpace(get(envDumbpipe)),
		ESOManifestDir:         strings.TrimSpace(get(envESOManifestDir)),
		ESONamespace:           strings.TrimSpace(get(envESONamespace)),
		ExternalSecrets:        strings.TrimSpace(get(envExternalSecrets)),
		ESOBundle:              strings.TrimSpace(get(envESOBundle)),
		GrantPath:              strings.TrimSpace(get(envGrant)),
		Interval:               DefaultInterval,
		LogLevel:               slog.LevelInfo,
		ScaleMaxNodes:          scale.DefaultMaxNodes,
		ScalePodsPerNode:       scale.DefaultPodsPerNode,
	}
	if cfg.GrantPath == "" {
		cfg.GrantPath = DefaultGrantPath
	}
	if cfg.UnitPrefix == "" {
		cfg.UnitPrefix = DefaultUnitPrefix
	}
	if cfg.HostProcessWorkspace == "" {
		cfg.HostProcessWorkspace = hostprocess.DefaultWorkspace
	}
	if cfg.MountContext == "" {
		cfg.MountContext = kcpclient.ShardAdminContext
	}
	cfg.CloudControllerManagerExec = inv.Reexec("internal", "cloud-controller-manager")
	cfg.FrontExec = inv.Reexec("internal", "apiserver-front")
	cfg.HostnameExec = inv.Reexec("internal", "ns-exec")
	if cfg.CloudControllerManager != "" {
		cfg.CloudControllerManagerExec = []string{cfg.CloudControllerManager}
	}
	if cfg.ESOManifestDir == "" {
		cfg.ESOManifestDir = DefaultESOManifestDir
	}
	if cfg.ESONamespace == "" {
		cfg.ESONamespace = eso.DefaultControllerNamespace
	}
	if cfg.AdvertiseAddress == "" && cfg.Substrate != SubstrateMarket {
		cfg.AdvertiseAddress = portallocator.LoopbackAdvertiseAddress
	}
	if cfg.MarketAdvertiseAddress == "" {
		cfg.MarketAdvertiseAddress = portallocator.MarketAdvertiseAddress
	}
	cfg.MarketDenyBidderDids = splitList(get(envMarketDenyBidderDids))
	switch {
	case cfg.StoreEndpoint != "" && cfg.Datastore != "" && cfg.StoreEndpoint != cfg.Datastore:
		return Config{}, fmt.Errorf(
			"%s is %q and %s is %q. They name ONE thing -- the store kine writes this cluster's "+
				"objects into -- and two answers to one question are not a preference to be "+
				"resolved silently: whichever lost would be a cluster whose objects went somewhere "+
				"nobody is looking", envStoreEndpoint, cfg.StoreEndpoint, envDatastore, cfg.Datastore)
	case cfg.StoreEndpoint == "":
		cfg.StoreEndpoint = cfg.Datastore
	}
	if raw := strings.TrimSpace(get(envPortRange)); raw != "" {
		rng, err := parsePortRange(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", envPortRange, err)
		}
		cfg.PortRange = rng
	}

	if raw := strings.TrimSpace(get(envScaleHold)); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s is not a duration: %w", envScaleHold, err)
		}
		if d < 0 {
			return Config{}, fmt.Errorf("%s must not be negative", envScaleHold)
		}
		if d == 0 {
			d = scale.NoHold
		}
		cfg.ScaleHold = d
	}

	if raw := strings.TrimSpace(get(envNodeJoinGrace)); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s is not a duration: %w", envNodeJoinGrace, err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("%s must be positive", envNodeJoinGrace)
		}
		cfg.NodeJoinGrace = d
	}

	if raw := strings.TrimSpace(get(envScaleMaxNodes)); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s is not a whole number: %w", envScaleMaxNodes, err)
		}
		if n < 1 {
			return Config{}, fmt.Errorf(
				"%s is %d, and a ceiling below one node is not a smaller cluster: it is a cluster "+
					"that can never place the work it has intent for. Leave it unset for the default "+
					"of %d", envScaleMaxNodes, n, scale.DefaultMaxNodes)
		}
		cfg.ScaleMaxNodes = n
	}

	if raw := strings.TrimSpace(get(envScalePodsPerNode)); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s is not a whole number: %w", envScalePodsPerNode, err)
		}
		if n < 1 {
			return Config{}, fmt.Errorf(
				"%s is %d, and a node that holds fewer than one pod is not a capacity to divide by. "+
					"Leave it unset for the default of %d", envScalePodsPerNode, n,
				scale.DefaultPodsPerNode)
		}
		cfg.ScalePodsPerNode = n
	}

	for _, req := range []struct{ name, value string }{
		{envKCPHost, cfg.KCPHost},
		{envKCPKubeconfig, cfg.KCPKubeconfig},
		{envLeasePath, cfg.LeasePath},
	} {
		if req.value == "" {
			return Config{}, fmt.Errorf("%s is not set", req.name)
		}
	}

	if cfg.Join != "" {
		if cfg.MintKubeconfigs == "" {
			return Config{}, fmt.Errorf(
				"%s is not set, so a join would bootstrap a tenant cluster and write no mint "+
					"credential: the gateway reads that directory and would fail every mint",
				envMintKubeconfigs)
		}
		raw := strings.TrimSpace(get(envDatastoreHasRow))
		if raw == "" {
			return Config{}, fmt.Errorf(
				"%s is not set. Whether the datastore already holds a bootstrap row is the "+
					"caller's OBSERVATION and has no default: a wrong \"false\" makes k3s exit "+
					"at boot with \"bootstrap data already found and encrypted with different "+
					"token\" (ADR 0023)", envDatastoreHasRow)
		}
		has, err := parseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", envDatastoreHasRow, err)
		}
		cfg.DatastoreHasRow = has

		if cfg.StateDir == "" {
			return Config{}, fmt.Errorf(
				"%s is not set. It holds this cluster's node records, its instance token, its "+
					"audit log and its identity bundle, and without it a gateway that restarts "+
					"can neither find nor reap the agent the last one started -- so it is required "+
					"whether or not %s names an external store", envStateDir, envStoreEndpoint)
		}
		if !filepath.IsAbs(cfg.StateDir) {
			return Config{}, fmt.Errorf(
				"%s is %q, which is relative. A node's identity IS the absolute socket path under "+
					"this directory -- that string is what a running VMM is recognised by, what a "+
					"reap compares against, and what a record written by an earlier gateway names. "+
					"A relative path makes that identity depend on the working directory the gateway "+
					"was started from, so the same node started from two directories yields two "+
					"identities, and the second one is invisible to the check that exists to stop two "+
					"VMMs running behind one node name", envStateDir, cfg.StateDir)
		}

		if err := cfg.checkPlacement(get); err != nil {
			return Config{}, err
		}

		if len(cfg.CloudControllerManagerExec) == 0 {
			return Config{}, fmt.Errorf(
				"%s is not set. Every joined cluster's process group carries the cloud controller "+
					"manager, which is what stamps a Node's providerID and the labels an autoscaler "+
					"groups by, and nothing else names that binary: a join that accepted an empty one "+
					"would lease, config and spawn everything else and silently drop the component "+
					"that gives a joined node its cloud identity", envCloudControllerManager)
		}

		if len(cfg.FrontExec) == 0 {
			return Config{}, errors.New(
				"the cluster provider is not running from the socialweb-computer binary, so no executable " +
					"carries `internal apiserver-front`: every joined cluster's apiserver port is owned by that " +
					"front, and k3s sits behind it on a slot port")
		}

		if cfg.ExternalSecrets == "" || cfg.ESOBundle == "" {
			return Config{}, fmt.Errorf(
				"%s and %s must both be set. Every joined cluster's process group carries the External "+
					"Secrets controller and its webhook beside the control plane: the first names the "+
					"pinned external-secrets binary, the second the pinned upstream bundle whose CRDs, "+
					"ClusterRoles and webhook registration the group installs into the tenant before "+
					"the pair starts. A join that accepted either empty would spawn a pair with nothing "+
					"to run or a tenant serving none of its kinds", envExternalSecrets, envESOBundle)
		}
	}

	var err error
	if cfg.TokenTimeout, err = optionalDuration(get, envTokenTimeout); err != nil {
		return Config{}, err
	}
	if cfg.MarketReleaseTimeout, err = optionalDuration(get, envMarketReleaseTimeout); err != nil {
		return Config{}, err
	}
	if cfg.LogLevel, err = level(get(envLogLevel)); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

const JoinAllClusters = "*"

func (c Config) ObserveOnly() bool { return c.Join == "" }

func (c Config) AllClusters() bool { return c.Join == JoinAllClusters }

func (c *Config) checkPlacement(get func(string) string) error {
	switch c.Substrate {
	case "":
		return fmt.Errorf(
			"%s is not set. Where a node runs is a decision this host cannot make for itself: "+
				"there is no way to observe it and no default to fall back on, because the answer "+
				"decides what a node's record names, what its teardown stops and what its lease "+
				"means -- a gateway that guessed would report a placement it is not honouring",
			envSubstrate)
	case SubstrateMarket:
		return c.checkMarketPlacement(get)
	default:
		return fmt.Errorf(
			"%s is %q, and %s is the only substrate implemented today: the local firecracker and "+
				"qemu substrates were retired on %s (plan 0005 M1-MARKET-ONLY) and the market network "+
				"replaces them -- a node is a VM rented from the compute market, born from the "+
				"cloud-init its winning bidder applies, so nothing about it runs on this host. A name "+
				"from before the retirement is refused rather than approximated: it would decide what "+
				"a node's record names, what its teardown stops and what its leases mean, and a "+
				"gateway running a placement nobody wrote would report a node it never booted, or one "+
				"it cannot stop. The names retired: %s",
			envSubstrate, c.Substrate, SubstrateMarket, RetiredSubstratesRetiredOn,
			strings.Join(retiredSubstrateNames, ", "))
	}
}

func (c *Config) checkMarketPlacement(get func(string) string) error {
	if c.AdvertiseAddress != "" {
		return fmt.Errorf(
			"%s is %q and %s is %s. A node on this substrate is a VM rented from the compute "+
				"market, and the address it dials its cluster's apiserver on is put on the guest's "+
				"OWN loopback and reverse-forwarded to this host by the market's door. That address "+
				"comes from %s, not from here, so a value in this variable cannot be in force: it is "+
				"refused rather than accepted and ignored, because an operator who set one and saw "+
				"no complaint would believe it was carrying it",
			envAdvertiseAddress, c.AdvertiseAddress, envSubstrate, c.Substrate, envMarketAdvertiseAddress)
	}
	if err := c.refuseRetiredSubstrateInputs(get); err != nil {
		return err
	}
	if err := c.checkHost(); err != nil {
		return err
	}
	if err := portallocator.AssertMarketAdvertiseAddress(c.MarketAdvertiseAddress); err != nil {
		return fmt.Errorf("%s: %w", envMarketAdvertiseAddress, err)
	}
	c.AdvertiseAddress = c.MarketAdvertiseAddress
	return nil
}

func (c *Config) refuseRetiredSubstrateInputs(get func(string) string) error {
	for _, name := range retiredSubstrateInputs {
		value := strings.TrimSpace(get(name))
		if value == "" {
			continue
		}
		return fmt.Errorf(
			"%s is %q, and %s is %s. That variable belonged to a local substrate -- a guest this "+
				"host started, wired and booted itself -- and those substrates were retired on %s "+
				"(plan 0005 M1-MARKET-ONLY): a market node is neither started nor wired on this host, "+
				"the market builds it from the cloud-init its winning bidder applies, and this "+
				"provider's whole part in it is the claim it writes. So the setting cannot be in "+
				"force, and it is refused rather than accepted and ignored -- an operator who set one "+
				"and saw no complaint would believe it was carrying it",
			name, value, envSubstrate, c.Substrate, RetiredSubstratesRetiredOn)
	}
	return nil
}

func (c Config) checkHost() error {
	host, err := os.Hostname()
	if err != nil {
		return fmt.Errorf(
			"%s is %q and this host's own name cannot be read (%w), so the comparison that "+
				"keeps a gateway from placing nodes on a machine it is not cannot be made. An "+
				"uncompared host is worse than an absent one: it would be accepted as a match",
			envHost, c.Host, err)
	}
	switch {
	case c.Host == "":
		return fmt.Errorf(
			"%s is not set. A gateway that places nodes has to be told which host it is, so that "+
				"the answer can be checked against os.Hostname() (%q); nothing here may assume, "+
				"because the assumption is accepted on the one machine where it is wrong",
			envHost, host)
	case c.Host != host:
		return fmt.Errorf(
			"%s is %q and this host calls itself %q. The mismatch is refused rather than "+
				"reconciled: a gateway told it is a host it is not would place nodes on the wrong "+
				"machine -- the node records and the tenant registrations would both name a "+
				"cluster running somewhere else, and every later pass would read that "+
				"other machine's state as its own",
			envHost, c.Host, host)
	}
	return nil
}

func splitList(raw string) []string {
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parsePortRange(raw string) (portallocator.Range, error) {
	first, last, ok := strings.Cut(raw, "-")
	if !ok {
		return portallocator.Range{}, fmt.Errorf("%q is not a FIRST-LAST port range", raw)
	}
	a, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil {
		return portallocator.Range{}, fmt.Errorf("%q is not a FIRST-LAST port range: %q is not a number", raw, strings.TrimSpace(first))
	}
	b, err := strconv.Atoi(strings.TrimSpace(last))
	if err != nil {
		return portallocator.Range{}, fmt.Errorf("%q is not a FIRST-LAST port range: %q is not a number", raw, strings.TrimSpace(last))
	}
	if b < a {
		return portallocator.Range{}, fmt.Errorf("%q runs backwards, and a backwards range reads as NO range at all: "+
			"the allocator would resolve it to its default carve and this process would hand out ports it believed "+
			"it had been told to avoid", raw)
	}
	return portallocator.Range{First: a, Last: b}, nil
}

func parseBool(raw string) (bool, error) {
	switch strings.ToLower(raw) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	}
	return false, fmt.Errorf("%q is not one of true, false", raw)
}

func optionalDuration(get func(string) string, name string) (time.Duration, error) {
	raw := strings.TrimSpace(get(name))
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration", name, raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: %q must be positive", name, raw)
	}
	return d, nil
}

func level(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("%s: %q is not one of debug, info, warn, error", envLogLevel, raw)
	}
}
