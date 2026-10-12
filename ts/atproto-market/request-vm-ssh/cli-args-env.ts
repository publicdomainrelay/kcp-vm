import {
  POLICY_CLI_OPTION,
  POLICY_ARGS_CLI_OPTION,
  ONLY_REMOTE_POLICY_EXEC_CLI_OPTION,
  ALLOW_UNTRUSTED_POLICY_EXEC_CLI_OPTION,
} from "@publicdomainrelay/policy-engine-cli-options";

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
    "atproto-oauth-qr": {
      type: "boolean" as const,
      description: "Use QR-based ATProto OAuth (scan with phone, session transferred via qr.fedfork.com). Alternative to --atproto-oauth loopback for headless environments.",
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
      description: "File the browser OAuth (--atproto-oauth) session is kept in; unset, it lives in the identity store at the MarketIdentity's sessionRef",
      env: "OAUTH_SESSION_PATH",
    },
    "atproto-identity-mode": {
      type: "string" as const,
      description: "MarketIdentity mode: localRepo (in-process repo + association records), oauthQr, oauthBrowser, pdsAccount or oauthDelegated. Unset derives it: --atproto-oauth is oauthBrowser, --atproto-oauth-qr is oauthQr, anything else is localRepo",
      env: "ATP_IDENTITY_MODE",
    },
    "market-identity-name": {
      type: "string" as const,
      description: "Name of the requester's MarketIdentity; names its vault paths and, with market-identity-kcp-server, the kcp object the sign-in follows",
      env: "MARKET_IDENTITY_NAME",
      default: "requester",
    },
    "market-identity-kcp-server": {
      type: "string" as const,
      description: "kcp server whose MarketIdentity object drives the sign-in (any mode; what the user must do is written to its status)",
      env: "MARKET_IDENTITY_KCP_SERVER",
    },
    "market-identity-kcp-workspace": {
      type: "string" as const,
      description: "kcp workspace holding the MarketIdentity object",
      env: "MARKET_IDENTITY_KCP_WORKSPACE",
    },
    "market-identity-kcp-token-file": {
      type: "string" as const,
      description: "File holding the bearer token for the kcp server, re-read on every call",
      env: "MARKET_IDENTITY_KCP_TOKEN_FILE",
    },
    "market-identity-kcp-ca-cert-file": {
      type: "string" as const,
      description: "PEM CA bundle the kcp server's certificate chains to",
      env: "MARKET_IDENTITY_KCP_CA_CERT_FILE",
    },
    "identity-vault-mount": {
      type: "string" as const,
      description: "Vault mount the requester's identity secrets live under",
      env: "IDENTITY_VAULT_MOUNT",
      default: "requester",
    },
    "identity-vault-file": {
      type: "string" as const,
      description: "Mode 600 JSON file used as the identity vault when no OpenBao is wired, keyed <mount>/<path>",
      env: "IDENTITY_VAULT_FILE",
    },
    "identity-vault-address": {
      type: "string" as const,
      description: "OpenBao address used as the identity vault (KV v2)",
      env: "IDENTITY_VAULT_ADDRESS",
    },
    "identity-vault-token-file": {
      type: "string" as const,
      description: "File holding the OpenBao token, re-read on every call",
      env: "IDENTITY_VAULT_TOKEN_FILE",
    },
    "identity-vault-ca-cert-file": {
      type: "string" as const,
      description: "PEM CA bundle the OpenBao certificate chains to",
      env: "IDENTITY_VAULT_CA_CERT_FILE",
    },
    "identity-vault-namespace": {
      type: "string" as const,
      description: "OpenBao namespace of the identity vault",
      env: "IDENTITY_VAULT_NAMESPACE",
    },
    "state-dir": {
      type: "string" as const,
      description: "This requester invocation's state directory; its identity store (keys and sessions) is <state-dir>/identity-vault.json unless an identity vault is named. Never $HOME",
      env: "STATE_DIR",
    },
    "qr-origin": {
      type: "string" as const,
      description: "QR relay origin the sign-in and association QR codes point at",
      env: "QR_ORIGIN",
    },
    "ingress-proxy-host": {
      type: "string" as const,
      description: "XRPC relay dispatcher host",
      env: "INGRESS_PROXY_HOST",
      default: "xrpc.fedproxy.com",
    },
    "no-ingress-proxy": {
      type: "boolean" as const,
      description: "Disable XRPC relay (rely on firehose-only for bid/accept/event delivery)",
    },
    "fedproxy-host": {
      type: "string" as const,
      description: "Removed (legacy-transport): iroh is the only guest transport; passing it is refused",
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
    "hold": {
      type: "boolean" as const,
      description:
        "Provisioning-only mode: after SSH-ready, hold the VM (no --exec session) until SIGTERM/SIGINT, then tear it down via vm.delete. For fleet plugins that run the job over their own SSH session.",
      env: "HOLD",
    },
    "ssh-authorized-key": {
      type: "string" as const,
      description:
        "Extra OpenSSH public key to authorize for root SSH, added alongside the requester's own ephemeral key (repeatable, or comma-separated). Lets a fleet plugin bring its own identity for ConnectInfo.",
      env: "SSH_AUTHORIZED_KEY",
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
    "guest-host-aliases": {
      type: "string" as const,
      description:
        "Comma-separated /etc/hosts entries for the guest, each '<ip> <name>' (e.g. '192.168.64.1 relay.localhost'). Lets the guest reach this requester and its bidder by a name that also resolves here.",
      env: "GUEST_HOST_ALIASES",
    },
    "user-data": {
      type: "string" as const,
      description: "Path to a base cloud-init file; the default iroh provisioning is patched into it before the RFP is sent",
      env: "USER_DATA",
    },
    "user-data-transport": {
      type: "string" as const,
      description: "Removed (legacy-transport): iroh is the only guest transport; passing it is refused",
    },
    "secrets": {
      type: "string" as const,
      description: "Path to a JSON file of [{\"path\": \"/some/path\", \"value\": \"secret-value\"}]. Non-string values (objects, arrays, numbers) are auto-stringified. Enables the secrets user-data plugin: an ephemeral server, reachable only through the ingress proxy, serves the bundle to the provisioned VM after it exchanges its workload identity token",
      env: "SECRETS_FILE",
    },
    "policy": POLICY_CLI_OPTION,
    "policy-args": POLICY_ARGS_CLI_OPTION,
    "policy-engine": {
      type: "string" as const,
      description: "Policy engine DID (e.g. did:web:engine.example.com). When set the minted policy record is a policies.service record evaluated by that engine",
      env: "POLICY_ENGINE",
    },
    "only-remote-policy-exec": ONLY_REMOTE_POLICY_EXEC_CLI_OPTION,
    "allow-untrusted-policy-exec": ALLOW_UNTRUSTED_POLICY_EXEC_CLI_OPTION,
    "pds-state-path": {
      type: "string" as const,
      description: "Path to persist PDS state (Deno.Kv SQLite file). If set, PDS state survives restarts and prior associations are remembered",
      env: "PDS_STATE_PATH",
      default: `${Deno.env.get("HOME") ?? Deno.env.get("USERPROFILE") ?? "/tmp"}/.cache/pdr-market/requester-pds`,
    },
    "oauth-session-file": {
      type: "string" as const,
      description: "File the QR OAuth (--atproto-oauth-qr) session is kept in, as session JSON; unset, it lives in the identity store at the MarketIdentity's sessionRef",
      env: "OAUTH_SESSION_FILE",
    },
    "oauth-session-client-id": {
      type: "string" as const,
      description:
        "OAuth client_id the session in --oauth-session-file was issued to. A refresh token is bound to the client that obtained it, so a session restored from a file must refresh as that client. Omit for sessions obtained through qr.fedfork.com.",
      env: "OAUTH_SESSION_CLIENT_ID",
    },
    "skip-qr": {
      type: "boolean" as const,
      description: "localRepo: no association (the MarketIdentity's association is Skip); without it the association QR and approval are pending actions on the MarketIdentity",
    },
    "skip-rbac": {
      type: "boolean" as const,
      description: "Skip writing the com.fedproxy.rbac record that authorizes the VM to register its SSH host key",
      env: "SKIP_RBAC",
    },
  },
};
