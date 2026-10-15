import {
  POLICY_CLI_OPTION,
  POLICY_ARGS_CLI_OPTION,
  ONLY_REMOTE_POLICY_EXEC_CLI_OPTION,
  ALLOW_UNTRUSTED_POLICY_EXEC_CLI_OPTION,
  POLICY_ENGINE_URL_CLI_OPTION,
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
      description: "File the browser OAuth (--atproto-oauth) session is kept in; unset, it lives in the identity store at the MarketIdentity's sessionRef",
      env: "OAUTH_SESSION_PATH",
    },
    "state-dir": {
      type: "string" as const,
      description: "This bidder instance's state directory, the one the application gives it. Never $HOME",
      env: "STATE_DIR",
    },
    "qr-origin": {
      type: "string" as const,
      description: "QR relay origin the sign-in and association QR codes point at",
      env: "QR_ORIGIN",
    },
    "network-hosts": {
      type: "string" as const,
      description: "Comma-separated name=address pairs of the market network's service names (its PDSes, PLC, relay) and the loopback address each answers on, copied from the network value; another application's names never resolve through this application's own kcp",
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
    "atproto-identity-mode": {
      type: "string" as const,
      description: "MarketIdentity mode: pdsAccount (sign in to --atproto-pds-url as --atproto-handle, records go to that PDS), localRepo (in-process repo + association records), oauthQr, oauthBrowser or oauthDelegated. Unset derives it: --atproto-oauth is oauthBrowser, --atproto-oauth-qr is oauthQr, a handle with a password or an identity vault is pdsAccount, anything else is localRepo",
      env: "ATP_IDENTITY_MODE",
    },
    "atproto-handle": {
      type: "string" as const,
      description: "AT Protocol handle of the bidder's account",
      env: "ATP_HANDLE",
    },
    "atproto-password": {
      type: "string" as const,
      description: "Account password for pdsAccount mode, development only; in production the password lives in the identity vault at <identity-vault-mount>/identity/<market-identity-name>/keys under the key password",
      env: "ATP_PASSWORD",
    },
    "atproto-pds-url": {
      type: "string" as const,
      description: "PDS the pdsAccount mode signs in to and writes the bidder's records to",
      env: "ATP_PDS_URL",
      default: "https://bsky.social",
    },
    "market-identity-name": {
      type: "string" as const,
      description: "Name of the bidder's MarketIdentity; names its vault paths and, with market-identity-kcp-server, the kcp object the sign-in follows",
      env: "MARKET_IDENTITY_NAME",
      default: "bidder",
    },
    "market-root-name": {
      type: "string" as const,
      description: "Name of the bidder's MarketBidder root object in the MarketIdentity's kcp workspace; the bidder writes its loop counters to its status.market",
      env: "MARKET_ROOT_NAME",
    },
    "market-identity-kcp-server": {
      type: "string" as const,
      description: "kcp server whose MarketIdentity object drives the sign-in (any mode; what the user must do is written to its status); requires an identity vault",
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
      env: "KCP_PROJECTED_KCP_TOKEN",
    },
    "market-identity-kcp-ca-cert-file": {
      type: "string" as const,
      description: "PEM CA bundle the kcp server's certificate chains to",
      env: "KCP_PROJECTED_KCP_CA",
    },
    "identity-vault-mount": {
      type: "string" as const,
      description: "Vault mount the bidder's identity secrets live under",
      env: "IDENTITY_VAULT_MOUNT",
      default: "bidder",
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
    "identity-vault-openbao-server": {
      type: "string" as const,
      description: "Name of the OpenBaoServer object beside this bidder; its status gives the vault's address, CA, client certificate and kv mount (the bidder's keys, relay keypairs, OIDC signing keys and sessions live there)",
      env: "IDENTITY_VAULT_OPENBAO_SERVER",
    },
    "identity-vault-openbao-client": {
      type: "string" as const,
      description: "Client of that OpenBaoServer the bidder logs in as (its cert role and policy)",
      env: "IDENTITY_VAULT_OPENBAO_CLIENT",
      default: "bidder",
    },
    "identity-vault-openbao-client-dir": {
      type: "string" as const,
      description: "Directory holding that client's certificate (tls.crt), key (tls.key) and the vault CA (ca.crt), kept current by the DenoPod's projected credentials; unset, they are read from the files the OpenBaoServer status names",
      env: "KCP_PROJECTED_VAULT",
    },
    "identity-vault-kcp-server": {
      type: "string" as const,
      description: "kcp server holding the OpenBaoServer object (a /clusters/<workspace> URL, or a base URL with identity-vault-kcp-workspace); a DenoPod's own KCP_SERVER",
      env: "KCP_SERVER",
    },
    "identity-vault-kcp-token": {
      type: "string" as const,
      description: "Bearer token for that kcp server; a DenoPod's own service account token",
      env: "KCP_TOKEN",
    },
    "identity-vault-kcp-workspace": {
      type: "string" as const,
      description: "kcp workspace holding the OpenBaoServer object, when identity-vault-kcp-server is a base URL",
      env: "IDENTITY_VAULT_KCP_WORKSPACE",
    },
    "identity-vault-kcp-token-file": {
      type: "string" as const,
      description: "File holding the bearer token for that kcp server, re-read on every call",
      env: "KCP_PROJECTED_KCP_TOKEN",
    },
    "identity-vault-kcp-ca-cert-file": {
      type: "string" as const,
      description: "PEM CA bundle that kcp server's certificate chains to",
      env: "KCP_PROJECTED_KCP_CA",
    },
    "identity-vault-namespace": {
      type: "string" as const,
      description: "OpenBao namespace of the identity vault",
      env: "IDENTITY_VAULT_NAMESPACE",
    },
    "compute-provider-kcp": {
      type: "boolean" as const,
      description: "Enable the kcp compute provider: each guest is a ComputeVM in this bidder's own kcp, booted by its ComputeVM controller",
      env: "COMPUTE_PROVIDER_KCP",
    },
    "compute-provider-kcp-server": {
      type: "string" as const,
      description: "kcp server of this bidder's own kcp, where the kcp provider writes ComputeVMs",
      env: "KCP_SERVER",
    },
    "compute-provider-kcp-workspace": {
      type: "string" as const,
      description: "kcp workspace the ComputeVMs live in",
      env: "KCP_WORKSPACE",
    },
    "compute-provider-kcp-token-file": {
      type: "string" as const,
      description: "File holding the bearer token for the kcp server, re-read on every call",
      env: "KCP_PROJECTED_KCP_TOKEN",
    },
    "compute-provider-kcp-ca-file": {
      type: "string" as const,
      description: "PEM CA bundle the kcp server's certificate chains to",
      env: "KCP_PROJECTED_KCP_CA",
    },
    "compute-provider-kcp-guest-login-mount": {
      type: "string" as const,
      description: "Auth mount of the sshsig plugin on this bidder's OpenBao; each guest gets a role there (written by the ComputeVM controller from spec.guestLogin) and binds its ssh host key to it once through the RFP cloud-init",
      env: "COMPUTE_PROVIDER_KCP_GUEST_LOGIN_MOUNT",
      default: "sshsig",
    },
    "compute-provider-kcp-guest-identity-resync-sec": {
      type: "number" as const,
      description: "Seconds between two reads of a live guest's requester RBAC records; a firehose event for the requester reads them at once, and this read catches what the firehose does not carry (a requester whose repo no relay crawls) and what it lost",
      env: "COMPUTE_PROVIDER_KCP_GUEST_IDENTITY_RESYNC_SEC",
      default: 30,
    },
    "compute-provider-kcp-handle": {
      type: "string" as const,
      description: "Owner handle written to every ComputeVM's spec.owner.handle; the controller namespaces units and work dirs by it",
      env: "COMPUTE_PROVIDER_KCP_HANDLE",
    },
    "compute-provider-kcp-guest-host-address": {
      type: "string" as const,
      description: "Address every guest dials to reach this host; unset, the constant guest host address of the ComputeVM API",
      env: "COMPUTE_PROVIDER_KCP_GUEST_HOST_ADDRESS",
    },
    "ingress-key-dir": {
      type: "string" as const,
      description: "Directory each of the bidder's dispatcher ingresses keeps its keypair in, so a restarted bidder registers the same subdomains its receipts and guests name",
      env: "INGRESS_KEY_DIR",
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
    "oauth-session-client-id": {
      type: "string" as const,
      description: "OAuth client_id a QR or delegated session was issued to; a refresh must use it. Omit for sessions obtained through qr.fedfork.com",
      env: "OAUTH_SESSION_CLIENT_ID",
    },
    "skip-qr": {
      type: "boolean" as const,
      description: "localRepo: no association (the MarketIdentity's association is Skip); without it the association QR and approval are pending actions on the MarketIdentity",
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
    "private-key-hex-path": {
      type: "string" as const,
      description: "Path to load/save the Secp256k1 private key hex. Creates file with generated key if missing. Overridden by --private-key-hex if both set.",
      env: "REPO_PRIVATE_KEY_HEX_PATH",
    },
    "pds-state-path": {
      type: "string" as const,
      description: "Path for PDS state persistence (Deno.Kv SQLite). Makes badgeBlueKeys associations survive restarts.",
      env: "PDS_STATE_PATH",
    },
    "policy": POLICY_CLI_OPTION,
    "policy-args": POLICY_ARGS_CLI_OPTION,
    "only-remote-policy-exec": ONLY_REMOTE_POLICY_EXEC_CLI_OPTION,
    "allow-untrusted-policy-exec": ALLOW_UNTRUSTED_POLICY_EXEC_CLI_OPTION,
    "policy-engine-url": POLICY_ENGINE_URL_CLI_OPTION,
    "offering-refresh-sec": {
      type: "number" as const,
      description: "Seconds between offering record re-commits to stay discoverable (0 to disable)",
      env: "OFFERING_REFRESH_SEC",
      default: 300,
    },
    "bid-window-sec": {
      type: "number" as const,
      description: "Seconds a published bid waits for an accept; after that its Bid object is Expired",
      env: "BID_WINDOW_SEC",
      default: 300,
    },
    "bid-quota": {
      type: "number" as const,
      description: "Most Bid objects the bidder holds; at that count it declines every RFP with reason capacity (0: no limit)",
      env: "BID_QUOTA",
      default: 0,
    },
    "computevm-quota": {
      type: "number" as const,
      description: "Most ComputeVM objects the bidder holds; at that count it declines every RFP with reason capacity (0: no limit)",
      env: "COMPUTEVM_QUOTA",
      default: 0,
    },
    "bid-ttl-sec": {
      type: "number" as const,
      description: "Seconds a finished Bid object lives before the TTL controller removes it, and after which the bidder removes its own market.bid record",
      env: "BID_TTL_SEC",
      default: 86400,
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
    "listeners-file": {
      type: "string" as const,
      description: "File every bound listener (tcp, unix, relay) is reported to as JSON once listening",
      env: "LISTENERS_FILE",
    },
    "status-file": {
      type: "string" as const,
      description: "File the bidder's DID, relay ingress and serve port are written to as JSON once it is ready",
      env: "STATUS_FILE",
    },
    "denopod-namespace": {
      type: "string" as const,
      description: "Namespace of the bidder DenoPod this process is the entry of, set by the deno runtime; unset means the bidder was not started by socialweb-computer run bidder",
      env: "KCP_NAMESPACE",
    },
    "denopod-service-domain": {
      type: "string" as const,
      description: "Service domain of the kcp the bidder DenoPod runs in, set by the deno runtime",
      env: "KCP_SERVICE_DOMAIN",
    },
  },
};
