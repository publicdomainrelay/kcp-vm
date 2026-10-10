use std::net::SocketAddr;
use std::path::PathBuf;

use clap::{Args, Parser, Subcommand, ValueEnum};

use crate::mesh::{DiscoveryChoice, RelayChoice};

pub const DEFAULT_MTU: u32 = 1160;
pub const DEFAULT_TUN_NAME: &str = "swmesh0";
pub const DEFAULT_SECRET_FILE: &str = "/var/lib/socialweb/meshd/secret";
pub const DEFAULT_METRICS_LISTEN: &str = "127.0.0.1:9469";

pub const EXIT_OK: i32 = 0;
pub const EXIT_RUNTIME: i32 = 1;
pub const EXIT_USAGE: i32 = 2;
pub const EXIT_IDENTITY_OR_PEERS: i32 = 3;
pub const EXIT_KUBE: i32 = 4;
pub const EXIT_NETWORK_DEVICE: i32 = 5;
pub const EXIT_ENDPOINT: i32 = 6;
pub const EXIT_STATUS_LISTEN: i32 = 7;

pub const EXIT_CODES_HELP: &str = "\
Exit codes:
  0  stopped by SIGTERM or SIGINT
  1  a running task ended (a source task or the outbound loop stopped)
  2  usage error
  3  the secret file or the peers file is unreadable or malformed
  4  the kubeconfig is malformed or refused (insecure-skip-tls-verify, no CA), or patching
     this node's own Node was forbidden (--node-name is not the kubeconfig's node)
  5  the tun device could not be created, brought up or read (needs CAP_NET_ADMIN and
     /dev/net/tun; a name already in use is refused). A route or address that fails to
     install is not fatal: it is retried on the next Node change, counted and logged
  6  the iroh endpoint could not bind
  7  the --metrics-listen address could not bind
Not fatal, retried: a missing kubeconfig, an unreachable apiserver, a list forbidden by
the node authorizer, an own Node not yet registered or without a podCIDR.";

pub const RUN_HELP: &str = "\
Node annotations written (kube mode) and read on every Node:
  socialweb.computer/mesh-endpoint  iroh endpoint id, 64 hex chars
  socialweb.computer/mesh-did       did:key of the same ed25519 key
  socialweb.computer/mesh-addrs     optional, read only: comma-separated direct ip:port hints

Status (loopback): GET /status (JSON), /metrics (Prometheus text), /healthz (200 once ready).
Ready means: tun up, own Node seen with a podCIDR, own annotations visible (kube mode), routes
synced. READY=1 is sent to $NOTIFY_SOCKET then (systemd Type=notify).";

#[derive(Debug, Parser)]
#[command(
    name = "socialweb-meshd",
    version,
    about = "Pod network for socialweb.computer market nodes: each peer Node's podCIDR is routed through a tun device to that Node's iroh endpoint, one IP packet per QUIC datagram",
    after_help = EXIT_CODES_HELP
)]
pub struct Cli {
    #[command(subcommand)]
    pub command: Command,
}

#[derive(Debug, Subcommand)]
pub enum Command {
    #[command(about = "Run the mesh daemon", after_help = format!("{RUN_HELP}\n\n{EXIT_CODES_HELP}"))]
    Run(Box<RunArgs>),
    #[command(
        about = "Print the endpoint id and did:key of the secret file as JSON, creating the secret (0600) if absent"
    )]
    Identity(IdentityArgs),
}

#[derive(Debug, Args)]
pub struct IdentityArgs {
    #[arg(long, env = "SOCIALWEB_MESHD_SECRET_FILE", default_value = DEFAULT_SECRET_FILE, help = "ed25519 secret as 64 hex chars (the IROH_SECRET encoding)")]
    pub secret_file: PathBuf,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, ValueEnum)]
pub enum DiscoveryArg {
    N0,
    None,
}

impl From<DiscoveryArg> for DiscoveryChoice {
    fn from(value: DiscoveryArg) -> DiscoveryChoice {
        match value {
            DiscoveryArg::N0 => DiscoveryChoice::N0,
            DiscoveryArg::None => DiscoveryChoice::None,
        }
    }
}

pub fn parse_relay(text: &str) -> Result<RelayChoice, String> {
    match text {
        "default" => Ok(RelayChoice::Default),
        "disabled" => Ok(RelayChoice::Disabled),
        urls => {
            let parsed: Result<Vec<iroh::RelayUrl>, String> = urls
                .split(',')
                .map(str::trim)
                .filter(|part| !part.is_empty())
                .map(|part| {
                    part.parse::<iroh::RelayUrl>()
                        .map_err(|err| format!("relay url {part:?}: {err}"))
                })
                .collect();
            let parsed = parsed?;
            if parsed.is_empty() {
                return Err("relay must be default, disabled or one or more URLs".to_string());
            }
            Ok(RelayChoice::Custom(parsed))
        }
    }
}

#[derive(Debug, Args)]
#[command(group(clap::ArgGroup::new("source").required(true).args(["kubeconfig", "peers_file"])))]
pub struct RunArgs {
    #[arg(
        long,
        env = "SOCIALWEB_MESHD_NODE_NAME",
        help = "This node's Kubernetes Node name (the kubelet's --hostname-override / node name)"
    )]
    pub node_name: String,

    #[arg(
        long,
        env = "SOCIALWEB_MESHD_KUBECONFIG",
        help = "Kubelet kubeconfig: reads all Nodes, patches its own Node's annotations"
    )]
    pub kubeconfig: Option<PathBuf>,

    #[arg(
        long,
        env = "SOCIALWEB_MESHD_PEERS_FILE",
        help = "Test mode instead of --kubeconfig: a NodeList JSON file (kubectl get nodes -o json shape), re-read when it changes; nothing is written"
    )]
    pub peers_file: Option<PathBuf>,

    #[arg(long, env = "SOCIALWEB_MESHD_SECRET_FILE", default_value = DEFAULT_SECRET_FILE, help = "ed25519 secret as 64 hex chars (the IROH_SECRET encoding); generated 0600 if absent")]
    pub secret_file: PathBuf,

    #[arg(long, env = "SOCIALWEB_MESHD_TUN_NAME", default_value = DEFAULT_TUN_NAME, help = "tun device to create")]
    pub tun_name: String,

    #[arg(long, env = "SOCIALWEB_MESHD_MTU", default_value_t = DEFAULT_MTU, help = "tun MTU; the default fits one IP packet into one QUIC datagram on a 1200-byte path")]
    pub mtu: u32,

    #[arg(long, env = "SOCIALWEB_MESHD_METRICS_LISTEN", default_value = DEFAULT_METRICS_LISTEN, help = "Status and metrics HTTP listen address; keep it on loopback")]
    pub metrics_listen: SocketAddr,

    #[arg(long, env = "SOCIALWEB_MESHD_RELAY", default_value = "default", value_parser = parse_relay, help = "iroh relays: default (n0 production relays), disabled, or comma-separated relay URLs")]
    pub relay: RelayChoice,

    #[arg(
        long,
        env = "SOCIALWEB_MESHD_DISCOVERY",
        value_enum,
        default_value = "n0",
        help = "iroh address lookup: n0 (publish and resolve through n0 DNS/pkarr) or none"
    )]
    pub discovery: DiscoveryArg,

    #[arg(
        long = "bind",
        env = "SOCIALWEB_MESHD_BIND",
        value_delimiter = ',',
        help = "UDP socket address(es) for iroh; default: iroh's own (all interfaces, ephemeral port)"
    )]
    pub bind: Vec<SocketAddr>,

    #[arg(
        long,
        env = "RUST_LOG",
        default_value = "info,iroh=warn,noq=warn,noq_proto=warn,iroh_relay=warn",
        help = "tracing filter for the JSON log lines on stderr"
    )]
    pub log_filter: String,
}

#[cfg(test)]
mod tests {
    use super::*;
    use clap::CommandFactory;

    #[test]
    fn the_cli_definition_is_consistent() {
        Cli::command().debug_assert();
    }

    #[test]
    fn run_needs_exactly_one_of_kubeconfig_and_peers_file() {
        let base = ["socialweb-meshd", "run", "--node-name", "a"];
        assert!(Cli::try_parse_from(base).is_err());
        let mut both = base.to_vec();
        both.extend(["--kubeconfig", "k", "--peers-file", "p"]);
        assert!(Cli::try_parse_from(both).is_err());
        let mut kube = base.to_vec();
        kube.extend([
            "--kubeconfig",
            "/var/lib/rancher/k3s/agent/kubelet.kubeconfig",
        ]);
        let parsed = Cli::try_parse_from(kube).unwrap();
        let Command::Run(args) = parsed.command else {
            panic!("not run")
        };
        assert_eq!(args.tun_name, DEFAULT_TUN_NAME);
        assert_eq!(args.mtu, DEFAULT_MTU);
        assert_eq!(args.relay, RelayChoice::Default);
        assert_eq!(args.discovery, DiscoveryArg::N0);
        assert!(args.bind.is_empty());
    }

    #[test]
    fn relay_accepts_default_disabled_or_urls() {
        assert_eq!(parse_relay("default").unwrap(), RelayChoice::Default);
        assert_eq!(parse_relay("disabled").unwrap(), RelayChoice::Disabled);
        match parse_relay("https://relay.example.com,https://r2.example.com").unwrap() {
            RelayChoice::Custom(urls) => assert_eq!(urls.len(), 2),
            other => panic!("{other:?}"),
        }
        assert!(parse_relay("not a url").is_err());
    }
}
