use std::collections::BTreeMap;
use std::fmt;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use base64::Engine;
use futures_util::StreamExt;
use rustls_pki_types::pem::PemObject;
use rustls_pki_types::{CertificateDer, PrivateKeyDer};
use serde::Deserialize;

use crate::nodes::{Node, NodeList, WatchEvent};

pub const WATCH_TIMEOUT_SECONDS: u64 = 290;
pub const REQUEST_TIMEOUT: Duration = Duration::from_secs(30);
pub const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
pub const WATCH_READ_TIMEOUT: Duration = Duration::from_secs(WATCH_TIMEOUT_SECONDS + 60);

#[derive(Debug)]
pub enum KubeError {
    Config(String),
    Transport(String),
    Status { code: u16, body: String },
    Decode(String),
}

impl KubeError {
    pub fn is_not_found(&self) -> bool {
        matches!(self, KubeError::Status { code: 404, .. })
    }

    pub fn is_gone(&self) -> bool {
        matches!(self, KubeError::Status { code: 410, .. })
    }
}

impl fmt::Display for KubeError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            KubeError::Config(msg) => write!(f, "kubeconfig: {msg}"),
            KubeError::Transport(msg) => write!(f, "kube api transport: {msg}"),
            KubeError::Status { code, body } => write!(f, "kube api status {code}: {body}"),
            KubeError::Decode(msg) => write!(f, "kube api decode: {msg}"),
        }
    }
}

impl std::error::Error for KubeError {}

#[derive(Debug, Default, Deserialize)]
struct KubeConfigFile {
    #[serde(rename = "current-context", default)]
    current_context: Option<String>,
    #[serde(default)]
    clusters: Vec<NamedCluster>,
    #[serde(default)]
    contexts: Vec<NamedContext>,
    #[serde(default)]
    users: Vec<NamedUser>,
}

#[derive(Debug, Deserialize)]
struct NamedCluster {
    name: String,
    cluster: ClusterEntry,
}

#[derive(Debug, Default, Deserialize)]
struct ClusterEntry {
    server: String,
    #[serde(rename = "certificate-authority", default)]
    certificate_authority: Option<String>,
    #[serde(rename = "certificate-authority-data", default)]
    certificate_authority_data: Option<String>,
    #[serde(rename = "insecure-skip-tls-verify", default)]
    insecure_skip_tls_verify: bool,
    #[serde(rename = "tls-server-name", default)]
    tls_server_name: Option<String>,
}

#[derive(Debug, Deserialize)]
struct NamedContext {
    name: String,
    context: ContextEntry,
}

#[derive(Debug, Deserialize)]
struct ContextEntry {
    cluster: String,
    #[serde(default)]
    user: Option<String>,
}

#[derive(Debug, Deserialize)]
struct NamedUser {
    name: String,
    #[serde(default)]
    user: UserEntry,
}

#[derive(Debug, Default, Deserialize)]
struct UserEntry {
    #[serde(rename = "client-certificate", default)]
    client_certificate: Option<String>,
    #[serde(rename = "client-certificate-data", default)]
    client_certificate_data: Option<String>,
    #[serde(rename = "client-key", default)]
    client_key: Option<String>,
    #[serde(rename = "client-key-data", default)]
    client_key_data: Option<String>,
    #[serde(default)]
    token: Option<String>,
    #[serde(rename = "tokenFile", default)]
    token_file: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ResolvedConfig {
    pub server: String,
    pub ca_pem: Option<Vec<u8>>,
    pub client_cert_pem: Option<Vec<u8>>,
    pub client_key_pem: Option<Vec<u8>>,
    pub token: Option<String>,
}

fn read_relative(base: &Path, file: &str) -> Result<Vec<u8>, KubeError> {
    let path = Path::new(file);
    let full: PathBuf = if path.is_absolute() {
        path.to_path_buf()
    } else {
        base.join(path)
    };
    std::fs::read(&full).map_err(|err| KubeError::Config(format!("read {}: {err}", full.display())))
}

fn inline_or_file(
    base: &Path,
    data: &Option<String>,
    file: &Option<String>,
    what: &str,
) -> Result<Option<Vec<u8>>, KubeError> {
    if let Some(encoded) = data {
        let decoded = base64::engine::general_purpose::STANDARD
            .decode(encoded.trim())
            .map_err(|err| KubeError::Config(format!("{what}-data is not base64: {err}")))?;
        return Ok(Some(decoded));
    }
    match file {
        Some(file) => Ok(Some(read_relative(base, file)?)),
        None => Ok(None),
    }
}

pub fn resolve_kubeconfig(text: &str, base: &Path) -> Result<ResolvedConfig, KubeError> {
    let file: KubeConfigFile =
        serde_yaml::from_str(text).map_err(|err| KubeError::Config(err.to_string()))?;
    let context = match &file.current_context {
        Some(name) if !name.is_empty() => Some(
            file.contexts
                .iter()
                .find(|ctx| &ctx.name == name)
                .ok_or_else(|| KubeError::Config(format!("current-context {name:?} not found")))?,
        ),
        _ => file.contexts.first(),
    };
    let cluster = match context {
        Some(ctx) => file
            .clusters
            .iter()
            .find(|c| c.name == ctx.context.cluster)
            .ok_or_else(|| {
                KubeError::Config(format!("cluster {:?} not found", ctx.context.cluster))
            })?,
        None => file
            .clusters
            .first()
            .ok_or_else(|| KubeError::Config("no clusters".to_string()))?,
    };
    let user = match context.and_then(|ctx| ctx.context.user.as_ref()) {
        Some(name) => Some(
            file.users
                .iter()
                .find(|u| &u.name == name)
                .ok_or_else(|| KubeError::Config(format!("user {name:?} not found")))?,
        ),
        None => file.users.first(),
    };
    if cluster.cluster.insecure_skip_tls_verify {
        return Err(KubeError::Config(
            "insecure-skip-tls-verify is refused: the node list decides where pod traffic goes"
                .to_string(),
        ));
    }
    if cluster.cluster.tls_server_name.is_some() {
        return Err(KubeError::Config(
            "tls-server-name is not supported".to_string(),
        ));
    }
    let entry = &cluster.cluster;
    let ca_pem = inline_or_file(
        base,
        &entry.certificate_authority_data,
        &entry.certificate_authority,
        "certificate-authority",
    )?;
    let empty = UserEntry::default();
    let user = user.map(|u| &u.user).unwrap_or(&empty);
    let client_cert_pem = inline_or_file(
        base,
        &user.client_certificate_data,
        &user.client_certificate,
        "client-certificate",
    )?;
    let client_key_pem =
        inline_or_file(base, &user.client_key_data, &user.client_key, "client-key")?;
    let token = match (&user.token, &user.token_file) {
        (Some(token), _) => Some(token.trim().to_string()),
        (None, Some(file)) => Some(
            String::from_utf8_lossy(&read_relative(base, file)?)
                .trim()
                .to_string(),
        ),
        (None, None) => None,
    };
    if client_cert_pem.is_some() != client_key_pem.is_some() {
        return Err(KubeError::Config(
            "client-certificate and client-key must be given together".to_string(),
        ));
    }
    Ok(ResolvedConfig {
        server: entry.server.trim_end_matches('/').to_string(),
        ca_pem,
        client_cert_pem,
        client_key_pem,
        token,
    })
}

fn tls_config(resolved: &ResolvedConfig) -> Result<rustls::ClientConfig, KubeError> {
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let mut roots = rustls::RootCertStore::empty();
    let Some(ca_pem) = &resolved.ca_pem else {
        return Err(KubeError::Config(
            "no certificate-authority: the apiserver must be verified against the cluster CA"
                .to_string(),
        ));
    };
    for cert in CertificateDer::pem_slice_iter(ca_pem) {
        let cert =
            cert.map_err(|err| KubeError::Config(format!("certificate-authority: {err}")))?;
        roots
            .add(cert)
            .map_err(|err| KubeError::Config(format!("certificate-authority: {err}")))?;
    }
    let builder = rustls::ClientConfig::builder_with_provider(provider)
        .with_safe_default_protocol_versions()
        .map_err(|err| KubeError::Config(err.to_string()))?
        .with_root_certificates(roots);
    let mut config = match (&resolved.client_cert_pem, &resolved.client_key_pem) {
        (Some(cert_pem), Some(key_pem)) => {
            let chain: Vec<CertificateDer<'static>> = CertificateDer::pem_slice_iter(cert_pem)
                .collect::<Result<_, _>>()
                .map_err(|err| KubeError::Config(format!("client-certificate: {err}")))?;
            let key = PrivateKeyDer::from_pem_slice(key_pem)
                .map_err(|err| KubeError::Config(format!("client-key: {err}")))?;
            builder
                .with_client_auth_cert(chain, key)
                .map_err(|err| KubeError::Config(format!("client certificate: {err}")))?
        }
        _ => builder.with_no_client_auth(),
    };
    config.alpn_protocols = vec![b"http/1.1".to_vec()];
    Ok(config)
}

#[derive(Clone)]
pub struct KubeClient {
    http: reqwest::Client,
    server: String,
    token: Option<String>,
}

impl KubeClient {
    pub fn from_kubeconfig(path: &Path) -> Result<KubeClient, KubeError> {
        let text = std::fs::read_to_string(path)
            .map_err(|err| KubeError::Config(format!("read {}: {err}", path.display())))?;
        let base = path.parent().unwrap_or_else(|| Path::new("."));
        let resolved = resolve_kubeconfig(&text, base)?;
        KubeClient::from_resolved(&resolved)
    }

    pub fn from_resolved(resolved: &ResolvedConfig) -> Result<KubeClient, KubeError> {
        let tls = tls_config(resolved)?;
        let http = reqwest::Client::builder()
            .tls_backend_preconfigured(tls)
            .connect_timeout(CONNECT_TIMEOUT)
            .read_timeout(WATCH_READ_TIMEOUT)
            .http1_only()
            .build()
            .map_err(|err| KubeError::Config(format!("http client: {err}")))?;
        Ok(KubeClient {
            http,
            server: resolved.server.clone(),
            token: resolved.token.clone(),
        })
    }

    fn authorize(&self, request: reqwest::RequestBuilder) -> reqwest::RequestBuilder {
        match &self.token {
            Some(token) => request.bearer_auth(token),
            None => request,
        }
    }

    async fn checked(response: reqwest::Response) -> Result<reqwest::Response, KubeError> {
        let status = response.status();
        if status.is_success() {
            return Ok(response);
        }
        let body = response.text().await.unwrap_or_default();
        Err(KubeError::Status {
            code: status.as_u16(),
            body: body.chars().take(512).collect(),
        })
    }

    pub async fn list_nodes(&self) -> Result<NodeList, KubeError> {
        let url = format!("{}/api/v1/nodes", self.server);
        let response = self
            .authorize(self.http.get(&url).timeout(REQUEST_TIMEOUT))
            .send()
            .await
            .map_err(|err| KubeError::Transport(err.to_string()))?;
        let response = KubeClient::checked(response).await?;
        let bytes = response
            .bytes()
            .await
            .map_err(|err| KubeError::Transport(err.to_string()))?;
        serde_json::from_slice(&bytes).map_err(|err| KubeError::Decode(err.to_string()))
    }

    pub async fn patch_node_annotations(
        &self,
        name: &str,
        annotations: &BTreeMap<String, String>,
    ) -> Result<(), KubeError> {
        let url = format!("{}/api/v1/nodes/{name}", self.server);
        let body = serde_json::json!({ "metadata": { "annotations": annotations } });
        let response = self
            .authorize(
                self.http
                    .patch(&url)
                    .timeout(REQUEST_TIMEOUT)
                    .header("Content-Type", "application/merge-patch+json")
                    .body(body.to_string()),
            )
            .send()
            .await
            .map_err(|err| KubeError::Transport(err.to_string()))?;
        KubeClient::checked(response).await?;
        Ok(())
    }

    pub async fn watch_nodes<F>(
        &self,
        resource_version: &str,
        mut on_event: F,
    ) -> Result<Option<String>, KubeError>
    where
        F: FnMut(NodeEvent),
    {
        let url = format!(
            "{}/api/v1/nodes?watch=true&allowWatchBookmarks=true&timeoutSeconds={WATCH_TIMEOUT_SECONDS}&resourceVersion={resource_version}",
            self.server
        );
        let response = self
            .authorize(self.http.get(&url))
            .send()
            .await
            .map_err(|err| KubeError::Transport(err.to_string()))?;
        let response = KubeClient::checked(response).await?;
        let mut stream = response.bytes_stream();
        let mut pending: Vec<u8> = Vec::new();
        let mut last_version: Option<String> = None;
        while let Some(chunk) = stream.next().await {
            let chunk = chunk.map_err(|err| KubeError::Transport(err.to_string()))?;
            pending.extend_from_slice(&chunk);
            while let Some(newline) = pending.iter().position(|b| *b == b'\n') {
                let line: Vec<u8> = pending.drain(..=newline).collect();
                if line.iter().all(u8::is_ascii_whitespace) {
                    continue;
                }
                match decode_watch_line(&line)? {
                    NodeEvent::Gone => {
                        return Err(KubeError::Status {
                            code: 410,
                            body: "watch expired".to_string(),
                        });
                    }
                    event => {
                        if let Some(version) = event.resource_version() {
                            last_version = Some(version.to_string());
                        }
                        on_event(event);
                    }
                }
            }
        }
        Ok(last_version)
    }
}

#[derive(Debug, PartialEq, Eq)]
pub enum NodeEvent {
    Upsert(Box<Node>),
    Delete(String, Option<String>),
    Bookmark(String),
    Gone,
}

impl NodeEvent {
    pub fn resource_version(&self) -> Option<&str> {
        match self {
            NodeEvent::Upsert(node) => node.metadata.resource_version.as_deref(),
            NodeEvent::Delete(_, version) => version.as_deref(),
            NodeEvent::Bookmark(version) => Some(version),
            NodeEvent::Gone => None,
        }
    }
}

pub fn decode_watch_line(line: &[u8]) -> Result<NodeEvent, KubeError> {
    let event: WatchEvent =
        serde_json::from_slice(line).map_err(|err| KubeError::Decode(err.to_string()))?;
    match event.kind.as_str() {
        "ADDED" | "MODIFIED" => {
            let node: Node = serde_json::from_value(event.object)
                .map_err(|err| KubeError::Decode(err.to_string()))?;
            Ok(NodeEvent::Upsert(Box::new(node)))
        }
        "DELETED" => {
            let node: Node = serde_json::from_value(event.object)
                .map_err(|err| KubeError::Decode(err.to_string()))?;
            Ok(NodeEvent::Delete(
                node.metadata.name,
                node.metadata.resource_version,
            ))
        }
        "BOOKMARK" => {
            let node: Node = serde_json::from_value(event.object)
                .map_err(|err| KubeError::Decode(err.to_string()))?;
            Ok(NodeEvent::Bookmark(
                node.metadata.resource_version.unwrap_or_default(),
            ))
        }
        "ERROR" => {
            let code = event
                .object
                .get("code")
                .and_then(|c| c.as_u64())
                .unwrap_or(0);
            if code == 410 {
                Ok(NodeEvent::Gone)
            } else {
                Err(KubeError::Status {
                    code: u16::try_from(code).unwrap_or(0),
                    body: event.object.to_string().chars().take(512).collect(),
                })
            }
        }
        other => Err(KubeError::Decode(format!(
            "unknown watch event type {other:?}"
        ))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const KUBELET_KUBECONFIG: &str = r#"apiVersion: v1
clusters:
- cluster:
    server: https://127.0.0.1:6444
    certificate-authority: server-ca.crt
  name: local
contexts:
- context:
    cluster: local
    namespace: default
    user: user
  name: Default
current-context: Default
kind: Config
preferences: {}
users:
- name: user
  user:
    client-certificate: /abs/client-kubelet.crt
    client-key: client-kubelet.key
"#;

    #[test]
    fn a_k3s_kubelet_kubeconfig_resolves_its_files_against_its_own_directory() {
        let dir = std::env::temp_dir().join(format!("meshd-kubeconfig-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("server-ca.crt"), b"CA").unwrap();
        std::fs::write(dir.join("client-kubelet.key"), b"KEY").unwrap();
        let text = KUBELET_KUBECONFIG.replace("/abs", dir.to_str().unwrap());
        std::fs::write(dir.join("client-kubelet.crt"), b"CERT").unwrap();
        let resolved = resolve_kubeconfig(&text, &dir).unwrap();
        assert_eq!(resolved.server, "https://127.0.0.1:6444");
        assert_eq!(resolved.ca_pem.as_deref(), Some(&b"CA"[..]));
        assert_eq!(resolved.client_cert_pem.as_deref(), Some(&b"CERT"[..]));
        assert_eq!(resolved.client_key_pem.as_deref(), Some(&b"KEY"[..]));
        assert_eq!(resolved.token, None);
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn inline_data_and_a_token_resolve_without_files() {
        let text = r#"
clusters:
- name: c
  cluster: {server: "https://10.0.0.1:6443/", certificate-authority-data: "Q0E="}
users:
- name: u
  user: {token: " abc "}
contexts:
- name: x
  context: {cluster: c, user: u}
current-context: x
"#;
        let resolved = resolve_kubeconfig(text, Path::new("/nonexistent")).unwrap();
        assert_eq!(resolved.server, "https://10.0.0.1:6443");
        assert_eq!(resolved.ca_pem.as_deref(), Some(&b"CA"[..]));
        assert_eq!(resolved.token.as_deref(), Some("abc"));
    }

    #[test]
    fn skipping_tls_verification_is_refused() {
        let text = r#"
clusters:
- name: c
  cluster: {server: "https://10.0.0.1:6443", insecure-skip-tls-verify: true}
"#;
        assert!(resolve_kubeconfig(text, Path::new("/")).is_err());
    }

    #[test]
    fn watch_lines_decode_into_upserts_deletes_bookmarks_and_expiry() {
        let added = br#"{"type":"ADDED","object":{"metadata":{"name":"a","resourceVersion":"5"},"spec":{"podCIDR":"10.42.1.0/24"}}}"#;
        match decode_watch_line(added).unwrap() {
            NodeEvent::Upsert(node) => {
                assert_eq!(node.metadata.name, "a");
                assert_eq!(node.spec.pod_cidr.as_deref(), Some("10.42.1.0/24"));
            }
            other => panic!("{other:?}"),
        }
        let deleted =
            br#"{"type":"DELETED","object":{"metadata":{"name":"b","resourceVersion":"6"}}}"#;
        assert_eq!(
            decode_watch_line(deleted).unwrap(),
            NodeEvent::Delete("b".to_string(), Some("6".to_string()))
        );
        let bookmark =
            br#"{"type":"BOOKMARK","object":{"kind":"Node","metadata":{"resourceVersion":"9"}}}"#;
        assert_eq!(
            decode_watch_line(bookmark).unwrap(),
            NodeEvent::Bookmark("9".to_string())
        );
        let gone = br#"{"type":"ERROR","object":{"kind":"Status","code":410,"reason":"Expired"}}"#;
        assert_eq!(decode_watch_line(gone).unwrap(), NodeEvent::Gone);
        let forbidden = br#"{"type":"ERROR","object":{"kind":"Status","code":403}}"#;
        assert!(decode_watch_line(forbidden).is_err());
    }
}
