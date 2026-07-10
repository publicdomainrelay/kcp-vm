import { POLICY_MODE_CLI_OPTION } from "@publicdomainrelay/market-policy-abc";

export default {
  name: "request-vm-ssh",
  description: "Compute market requester: create RFPs, collect bids, provision VMs, SSH in",
  options: {
    "port": {
      type: "number" as const,
      description: "TCP port to listen on (omit to serve only through xrpc.fedproxy.com relay)",
      env: "PORT",
    },
    "private-key-hex": {
      type: "string" as const,
      description: "Secp256k1 private key hex for the requester DID",
      env: "REPO_PRIVATE_KEY_HEX",
    },
    "private-key-hex-path": {
      type: "string" as const,
      description: "Path to load/save the Secp256k1 private key hex. Creates file with generated key if missing or empty. Overridden by --private-key-hex if both are set.",
      env: "REPO_PRIVATE_KEY_HEX_PATH",
      default: `${Deno.env.get("HOME") ?? Deno.env.get("USERPROFILE") ?? "/tmp"}/.cache/pdr-market/requester-private-key`,
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
    "atproto-handle": {
      type: "string" as const,
      description: "AT Protocol handle for OAuth login",
      env: "ATP_HANDLE",
    },
    "oauth-client-id": {
      type: "string" as const,
      description: "OAuth client ID URL. Defaults to loopback http://localhost.",
      env: "OAUTH_CLIENT_ID",
      default: "http://localhost",
    },
    "oauth-redirect-uri": {
      type: "string" as const,
      description: "OAuth loopback redirect URI. Port 0 = random.",
      env: "OAUTH_REDIRECT_URI",
      default: "http://127.0.0.1:0/callback",
    },
    "oauth-session-path": {
      type: "string" as const,
      description: "Path to persist the OAuth session JSON",
      env: "OAUTH_SESSION_PATH",
      default: `${Deno.env.get("HOME") ?? Deno.env.get("USERPROFILE") ?? "/tmp"}/.cache/pdr-market/requester-oauth-session.json`,
    },
    "ingress-proxy-host": {
      type: "string" as const,
      description: "XRPC relay dispatcher host",
      env: "INGRESS_PROXY_HOST",
      default: "xrpc.fedproxy.com",
    },
    "fedproxy-host": {
      type: "string" as const,
      description: "FedProxy SSH-tunnel host where the guest publishes itself for SSH (vmName--did-plc-<key>.<fedproxy-host>)",
      env: "FEDPROXY_HOST",
      default: "fedproxy.com",
    },
    "label": {
      type: "string" as const,
      description: "Label for log and relay identification",
      env: "REQUESTER_LABEL",
      default: "request-vm-ssh",
    },
    "vm-name": {
      type: "string" as const,
      description: "VM name (default: auto-generated compute-<random hex>)",
      env: "VM_NAME",
    },
    "bid-window-sec": {
      type: "number" as const,
      description: "Seconds to wait for bids after submitting RFP",
      env: "BID_WINDOW_SEC",
      default: 30,
    },
    "exec": {
      type: "string" as const,
      description: "Program to run in the VM (for non-TTY sessions)",
      env: "EXEC_PROGRAM",
      default: "bash",
    },
    "vm-ready-timeout-sec": {
      type: "number" as const,
      description: "Timeout in seconds waiting for VM SSH readiness",
      env: "VM_READY_TIMEOUT_SEC",
      default: 300,
    },
    "keep-vm": {
      type: "boolean" as const,
      description: "Keep VM after SSH session (skip delete)",
      env: "KEEP_VM",
    },
    "skip-ssh": {
      type: "boolean" as const,
      description: "Skip SSH wait and session (for testing)",
      env: "SKIP_SSH",
    },
    "bidder-dids": {
      type: "string" as const,
      description: "Comma-separated bidder DIDs",
      env: "BIDDER_DIDS",
    },
    "deny-bidder-dids": {
      type: "string" as const,
      description: "Comma-separated DIDs to exclude from bidding",
      env: "DENY_BIDDER_DIDS",
    },
    "relay-url": {
      type: "string" as const,
      description: "Relay URL for bidder discovery via listReposByCollection",
      env: "RELAY_URL",
      default: "https://reg.market.fedfork.com",
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
    "serve-addr": {
      type: "string" as const,
      description: "TCP address to bind the requester PDS (only used when --port is set)",
      env: "SERVE_ADDR",
    },
    "serve-unix": {
      type: "string" as const,
      description: "Unix socket path to serve on (optional, in addition to TCP)",
      env: "SERVE_UNIX",
    },
    "user-data": {
      type: "string" as const,
      description: "Path to a base cloud-init file; the default websocat/fedproxy-client provisioning is patched into it before the RFP is sent",
      env: "USER_DATA",
    },
    "policy-mode": POLICY_MODE_CLI_OPTION,
    "policy-engine-endpoint": {
      type: "string" as const,
      description: "Policy engine service DID ref for dynamic mode (e.g. did:web:engine.example.com#market_evaluate_policy or did:plc:xyz#market_evaluate_policy)",
      env: "POLICY_ENGINE_ENDPOINT",
    },
    "pds-state-path": {
      type: "string" as const,
      description: "Path to persist PDS state (Deno.Kv SQLite file). If set, PDS state survives restarts and prior associations are remembered",
      env: "PDS_STATE_PATH",
      default: `${Deno.env.get("HOME") ?? Deno.env.get("USERPROFILE") ?? "/tmp"}/.cache/pdr-market/requester-pds`,
    },
    "no-qr": {
      type: "boolean" as const,
      description: "Skip QR code and association confirmation prompt",
    },
    "skip-rbac": {
      type: "boolean" as const,
      description: "Skip writing the com.fedproxy.rbac record that authorizes the VM to register its SSH host key",
      env: "SKIP_RBAC",
    },
  },
};
