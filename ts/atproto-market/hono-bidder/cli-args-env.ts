import {
  POLICY_CLI_OPTION,
  POLICY_ARGS_CLI_OPTION,
  ONLY_REMOTE_POLICY_EXEC_CLI_OPTION,
  ALLOW_UNTRUSTED_POLICY_EXEC_CLI_OPTION,
} from "@publicdomainrelay/policy-engine-cli-options";

export default {
  name: "bidder",
  description: "Compute bidder for market contract testing",
  options: {
    "service-name": {
      type: "string" as const,
      description: "Service name for logging",
      env: "SERVICE_NAME",
      default: "bidder",
    },
    "private-key-hex": {
      type: "string" as const,
      description: "Secp256k1 private key hex for the bidder DID",
      env: "REPO_PRIVATE_KEY_HEX",
    },
    "plc-directory-url": {
      type: "string" as const,
      description: "PLC directory URL for DID registration",
      env: "PLC_DIRECTORY_URL",
      default: "https://plc.directory",
    },
    "atproto-oauth": {
      type: "boolean" as const,
      description: "Use ATProto OAuth login instead of local PDS. Requires --atproto-handle.",
    },
    "atproto-oauth-qr": {
      type: "boolean" as const,
      description: "Use QR-based ATProto OAuth (scan with phone, session transferred via qr.fedfork.com). Alternative to --atproto-oauth loopback for headless environments.",
    },
    "oauth-client-id": {
      type: "string" as const,
      description: "OAuth client ID URL for client metadata. Defaults to loopback http://localhost per ATProto CLI OAuth pattern.",
      env: "OAUTH_CLIENT_ID",
      default: "http://localhost",
    },
    "oauth-redirect-uri": {
      type: "string" as const,
      description: "OAuth loopback redirect URI. Port 0 = random available port.",
      env: "OAUTH_REDIRECT_URI",
      default: "http://127.0.0.1:0/callback",
    },
    "oauth-session-path": {
      type: "string" as const,
      description: "Path to persist the OAuth session JSON",
      env: "OAUTH_SESSION_PATH",
      default: `${Deno.env.get("HOME") ?? Deno.env.get("USERPROFILE") ?? "/tmp"}/.cache/pdr-market/bidder-oauth-session.json`,
    },
    "ingress-proxy-host": {
      type: "string" as const,
      description: "XRPC relay dispatcher host",
      env: "INGRESS_PROXY_HOST",
      default: "xrpc.fedproxy.com",
    },
    "relay-url": {
      type: "string" as const,
      description: "AT Protocol relay URL for requestCrawl registration and bidder discovery",
      env: "RELAY_URL",
    },
    "atproto-handle": {
      type: "string" as const,
      description: "AT Protocol handle for remote PDS login",
      env: "ATP_HANDLE",
    },
    "atproto-password": {
      type: "string" as const,
      description: "AT Protocol password for remote PDS login",
      env: "ATP_PASSWORD",
    },
    "atproto-pds-url": {
      type: "string" as const,
      description: "AT Protocol PDS URL for remote login",
      env: "ATP_PDS_URL",
      default: "https://bsky.social",
    },
    "compute-provider-digitalocean-token": {
      type: "string" as const,
      description: "DigitalOcean API token",
      env: "COMPUTE_PROVIDER_DO_TOKEN",
    },
    "compute-provider-digitalocean-base-url": {
      type: "string" as const,
      description: "DigitalOcean API base URL",
      env: "COMPUTE_PROVIDER_DO_BASE_URL",
    },
    "compute-provider-local": {
      type: "boolean" as const,
      description: "Enable local compute provider",
    },
    "compute-provider-local-mode": {
      type: "string" as const,
      description: "Local provider mode: container or vm (default: container)",
      env: "COMPUTE_PROVIDER_LOCAL_MODE",
    },
    "compute-provider-local-vm-image": {
      type: "string" as const,
      description: "Local provider VM image",
      env: "COMPUTE_PROVIDER_LOCAL_VM_IMAGE",
    },
    "compute-provider-local-container-image": {
      type: "string" as const,
      description: "Local provider container image",
      env: "COMPUTE_PROVIDER_LOCAL_CONTAINER_IMAGE",
    },
    "compute-provider-local-cache-dir": {
      type: "string" as const,
      description: "Local provider cache directory",
      env: "COMPUTE_PROVIDER_LOCAL_CACHE_DIR",
    },
    "compute-provider-firecracker": {
      type: "boolean" as const,
      description: "Enable the Firecracker compute provider",
    },
    "compute-provider-firecracker-nodeimage": {
      type: "string" as const,
      description: "Path to the socialweb-nodeimage binary, which builds the image a guest boots from",
      env: "COMPUTE_PROVIDER_FIRECRACKER_NODEIMAGE",
    },
    "compute-provider-firecracker-nodeboot": {
      type: "string" as const,
      description: "Path to the socialweb-nodeboot binary, which boots one guest",
      env: "COMPUTE_PROVIDER_FIRECRACKER_NODEBOOT",
    },
    "compute-provider-firecracker-config": {
      type: "string" as const,
      description: "Path to the image builder's nodeimage.json",
      env: "COMPUTE_PROVIDER_FIRECRACKER_CONFIG",
    },
    "compute-provider-firecracker-repo-dir": {
      type: "string" as const,
      description: "Path to the socialweb-computer-kcp checkout, which the image builder builds the guest agent from",
      env: "COMPUTE_PROVIDER_FIRECRACKER_REPO_DIR",
    },
    "compute-provider-firecracker-preinstall": {
      type: "string" as const,
      description: "Path to the preinstall manifest naming the software baked into the image",
      env: "COMPUTE_PROVIDER_FIRECRACKER_PREINSTALL",
    },
    "compute-provider-firecracker-vmm": {
      type: "string" as const,
      description: "Path to the firecracker binary",
      env: "COMPUTE_PROVIDER_FIRECRACKER_VMM",
    },
    "compute-provider-firecracker-work-root": {
      type: "string" as const,
      description: "Directory each guest gets a work directory under",
      env: "COMPUTE_PROVIDER_FIRECRACKER_WORK_ROOT",
    },
    "compute-provider-firecracker-range-base": {
      type: "string" as const,
      description: "Base address guests are addressed from, one /30 each",
      env: "COMPUTE_PROVIDER_FIRECRACKER_RANGE_BASE",
    },
    "compute-provider-firecracker-reuse-image": {
      type: "boolean" as const,
      description: "Use the image already in the store and skip the rebuild when the inputs have moved",
    },
    "compute-provider-deno-worker": {
      type: "boolean" as const,
      description: "Enable Deno worker compute provider",
    },
    "worker-permission-mode": {
      type: "string" as const,
      description: "Worker permission policy: deny-all (default) or allow-net",
      env: "WORKER_PERMISSION_MODE",
    },
    "no-ingress-proxy": {
      type: "boolean" as const,
      description: "Disable XRPC relay on main serve",
    },
    "serve-addr": {
      type: "string" as const,
      description: "Address to serve on",
      env: "SERVE_ADDR",
      default: "0.0.0.0",
    },
    "serve-port": {
      type: "number" as const,
      description: "Port to listen on (0 = random)",
      env: "SERVE_PORT",
      default: 0,
    },
    "serve-unix": {
      type: "string" as const,
      description: "Unix socket path",
      env: "SERVE_UNIX",
    },
    "firehose-mode": {
      type: "string" as const,
      description: "Firehose transport: subscriberepos, jetstream, or off",
      env: "FIREHOSE_MODE",
      default: "off",
    },
    "firehose-url": {
      type: "string" as const,
      description: "Firehose websocket URL (repeatable, or comma-separated list for multiple relays)",
      env: "FIREHOSE_URL",
    },
    "oauth-session-file": {
      type: "string" as const,
      description: "Path to OAuth QR session file (overrides default cache path). When set with --atproto-oauth-qr, session is loaded from this file instead of the default ~/.cache/pdr-market/ location.",
      env: "OAUTH_SESSION_FILE",
    },
    "skip-qr": {
      type: "boolean" as const,
      description: "Skip QR code display and association prompt",
      env: "SKIP_QR",
    },
    "associate-with": {
      type: "string" as const,
      description: "Operator DID to mint a bidder_associate badgeBlueKeys record for at boot, so the trust cache resolves this bidder's operator to that DID before the only-me scope check runs. Mirrors the QR associate flow for headless/automation.",
      env: "ASSOCIATE_WITH",
    },
    "guest-tls-port": {
      type: "number" as const,
      description: "TLS listener port of the local dispatcher. Guest OIDC URLs (HTTPS-only) are rewritten to this port; the bidder's own traffic stays on the plain ingress-proxy-host port.",
      env: "GUEST_TLS_PORT",
    },
    "ca-cert-pem": {
      type: "string" as const,
      description: "PEM CA certificate to inject into provisioned guest containers (trust store). Used with self-signed *.localhost certs.",
      env: "CA_CERT_PEM",
    },
    "private-key-hex-path": {
      type: "string" as const,
      description: "Path to load/save the Secp256k1 private key hex. Creates file with generated key if missing. Overridden by --private-key-hex if both set.",
      env: "REPO_PRIVATE_KEY_HEX_PATH",
      default: `${Deno.env.get("HOME") ?? Deno.env.get("USERPROFILE") ?? "/tmp"}/.cache/pdr-market/bidder-private-key`,
    },
    "pds-state-path": {
      type: "string" as const,
      description: "Path for PDS state persistence (Deno.Kv SQLite). Makes badgeBlueKeys associations survive restarts.",
      env: "PDS_STATE_PATH",
      default: `${Deno.env.get("HOME") ?? Deno.env.get("USERPROFILE") ?? "/tmp"}/.cache/pdr-market/bidder-pds`,
    },
    "policy": POLICY_CLI_OPTION,
    "policy-args": POLICY_ARGS_CLI_OPTION,
    "only-remote-policy-exec": ONLY_REMOTE_POLICY_EXEC_CLI_OPTION,
    "allow-untrusted-policy-exec": ALLOW_UNTRUSTED_POLICY_EXEC_CLI_OPTION,
    "offering-refresh-sec": {
      type: "number" as const,
      description: "Seconds between offering record re-commits to stay discoverable (0 to disable)",
      env: "OFFERING_REFRESH_SEC",
      default: 300,
    },
    "tls-cert-file": {
      type: "string" as const,
      description: "PEM certificate to serve TLS with; requires tls-key-file",
      env: "TLS_CERT_FILE",
    },
    "tls-key-file": {
      type: "string" as const,
      description: "PEM private key to serve TLS with; requires tls-cert-file",
      env: "TLS_KEY_FILE",
    },
    "port-file": {
      type: "string" as const,
      description: "File the bound TCP port is written to once listening (used with port 0)",
      env: "PORT_FILE",
      default: "./port-bound-to",
    },
  },
};
