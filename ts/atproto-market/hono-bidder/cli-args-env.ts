import { POLICY_MODE_CLI_OPTION } from "@publicdomainrelay/market-policy-abc";

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
    "skip-qr": {
      type: "boolean" as const,
      description: "Skip QR code display and association prompt",
      env: "SKIP_QR",
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
    "policy-mode": POLICY_MODE_CLI_OPTION,
    "offering-refresh-sec": {
      type: "number" as const,
      description: "Seconds between offering record re-commits to stay discoverable (0 to disable)",
      env: "OFFERING_REFRESH_SEC",
      default: 300,
    },
  },
};
