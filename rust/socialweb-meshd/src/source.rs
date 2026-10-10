use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{Duration, SystemTime};

use tokio::sync::watch;

use crate::kube::{KubeClient, KubeError, NodeEvent};
use crate::nodes::{NodeList, NodeSet};
use crate::routes::{ANNOTATION_DID, ANNOTATION_ENDPOINT, EndpointBytes, RouteTable};
use crate::stats::Stats;

pub const PEERS_FILE_POLL: Duration = Duration::from_secs(2);
pub const KUBE_RETRY_MIN: Duration = Duration::from_secs(1);
pub const KUBE_RETRY_MAX: Duration = Duration::from_secs(30);
pub const ANNOTATE_RETRY: Duration = Duration::from_secs(5);
pub const NODE_READER_HINT: &str = "the node authorizer lets a node read only its own Node (AuthorizeNodeWithSelectors); apply rust/socialweb-meshd/deploy/node-reader-rbac.yaml to grant group system:nodes get/list/watch on nodes";

#[derive(Clone)]
pub struct SelfIdentity {
    pub node_name: String,
    pub endpoint: EndpointBytes,
    pub endpoint_hex: String,
    pub did: String,
}

#[derive(Debug)]
pub enum SourceError {
    Config(String),
    Forbidden(String),
}

impl std::fmt::Display for SourceError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            SourceError::Config(msg) => write!(f, "{msg}"),
            SourceError::Forbidden(msg) => write!(f, "{msg}"),
        }
    }
}

fn publish(
    me: &SelfIdentity,
    nodes: &NodeSet,
    tx: &watch::Sender<Arc<RouteTable>>,
    stats: &Stats,
) -> Arc<RouteTable> {
    let table = Arc::new(RouteTable::compute(
        &me.node_name,
        &me.endpoint,
        &nodes.to_vec(),
    ));
    Stats::bump(&stats.node_list_updates);
    let changed = **tx.borrow() != *table;
    if changed {
        tracing::info!(
            event = "routes",
            own_pod_cidrs = ?table.own_cidrs.iter().map(ToString::to_string).collect::<Vec<_>>(),
            routes = ?table.routes.iter().map(|r| format!("{}->{}", r.cidr, r.node)).collect::<Vec<_>>(),
            rejected = ?table.rejected.iter().map(|r| format!("{}: {}", r.node, r.reason)).collect::<Vec<_>>()
        );
        tx.send_replace(Arc::clone(&table));
    }
    table
}

pub fn read_peers_file(path: &Path) -> Result<NodeList, String> {
    let bytes = std::fs::read(path).map_err(|err| format!("read {}: {err}", path.display()))?;
    serde_json::from_slice(&bytes)
        .map_err(|err| format!("parse {} as a NodeList: {err}", path.display()))
}

fn modified(path: &Path) -> Option<SystemTime> {
    std::fs::metadata(path).and_then(|m| m.modified()).ok()
}

pub async fn run_peers_file(
    path: PathBuf,
    me: SelfIdentity,
    tx: watch::Sender<Arc<RouteTable>>,
    stats: Arc<Stats>,
    first: NodeList,
) {
    let mut seen = modified(&path);
    publish(&me, &NodeSet::from_list(first), &tx, &stats);
    loop {
        tokio::time::sleep(PEERS_FILE_POLL).await;
        let now = modified(&path);
        if now == seen {
            continue;
        }
        seen = now;
        match read_peers_file(&path) {
            Ok(list) => {
                publish(&me, &NodeSet::from_list(list), &tx, &stats);
            }
            Err(err) => tracing::warn!(event = "peers_file_unreadable", error = %err),
        }
    }
}

fn annotations(me: &SelfIdentity) -> BTreeMap<String, String> {
    BTreeMap::from([
        (ANNOTATION_ENDPOINT.to_string(), me.endpoint_hex.clone()),
        (ANNOTATION_DID.to_string(), me.did.clone()),
    ])
}

pub fn kube_client(path: &Path) -> Result<Option<KubeClient>, SourceError> {
    if !path.exists() {
        return Ok(None);
    }
    match KubeClient::from_kubeconfig(path) {
        Ok(client) => Ok(Some(client)),
        Err(KubeError::Config(msg)) if msg.starts_with("read ") => Ok(None),
        Err(err) => Err(SourceError::Config(err.to_string())),
    }
}

pub async fn run_annotator(
    kubeconfig: PathBuf,
    me: SelfIdentity,
    mut routes: watch::Receiver<Arc<RouteTable>>,
) -> Result<(), SourceError> {
    loop {
        let needs = {
            let table = routes.borrow_and_update();
            table.self_view.seen && !table.self_annotated_with(&me.endpoint_hex, &me.did)
        };
        if needs {
            if let Some(client) = kube_client(&kubeconfig)? {
                match client
                    .patch_node_annotations(&me.node_name, &annotations(&me))
                    .await
                {
                    Ok(()) => {
                        tracing::info!(event = "node_annotated", node = %me.node_name, endpoint = %me.endpoint_hex, did = %me.did);
                    }
                    Err(KubeError::Status { code: 403, body }) => {
                        return Err(SourceError::Forbidden(format!(
                            "patching Node {} was forbidden ({body}); --node-name must be the node the kubeconfig's identity is",
                            me.node_name
                        )));
                    }
                    Err(err) => {
                        tracing::warn!(event = "annotate_failed", node = %me.node_name, error = %err);
                    }
                }
            }
            tokio::select! {
                changed = routes.changed() => if changed.is_err() { return Ok(()) },
                () = tokio::time::sleep(ANNOTATE_RETRY) => {}
            }
        } else if routes.changed().await.is_err() {
            return Ok(());
        }
    }
}

pub async fn run_kube(
    kubeconfig: PathBuf,
    me: SelfIdentity,
    tx: watch::Sender<Arc<RouteTable>>,
    stats: Arc<Stats>,
) -> Result<(), SourceError> {
    let mut retry = KUBE_RETRY_MIN;
    loop {
        let client = match kube_client(&kubeconfig)? {
            Some(client) => client,
            None => {
                tracing::info!(event = "kubeconfig_waiting", path = %kubeconfig.display());
                tokio::time::sleep(retry).await;
                retry = (retry * 2).min(KUBE_RETRY_MAX);
                continue;
            }
        };
        let list = match client.list_nodes().await {
            Ok(list) => list,
            Err(KubeError::Status { code: 403, body }) => {
                tracing::error!(
                    event = "list_nodes_forbidden",
                    error = %body,
                    hint = NODE_READER_HINT,
                    retry_in_ms = retry.as_millis() as u64
                );
                tokio::time::sleep(retry).await;
                retry = (retry * 2).min(KUBE_RETRY_MAX);
                continue;
            }
            Err(err) => {
                tracing::warn!(event = "list_nodes_failed", error = %err, retry_in_ms = retry.as_millis() as u64);
                tokio::time::sleep(retry).await;
                retry = (retry * 2).min(KUBE_RETRY_MAX);
                continue;
            }
        };
        retry = KUBE_RETRY_MIN;
        let version = list.metadata.resource_version.clone().unwrap_or_default();
        let mut nodes = NodeSet::from_list(list);
        let table = publish(&me, &nodes, &tx, &stats);
        if !table.self_view.seen {
            tracing::info!(event = "own_node_waiting", node = %me.node_name);
        }

        let watch_started = tokio::time::Instant::now();
        let watched = client
            .watch_nodes(&version, |event| {
                let dirty = match event {
                    NodeEvent::Upsert(node) => {
                        nodes.upsert(*node);
                        true
                    }
                    NodeEvent::Delete(name, _) => {
                        nodes.remove(&name);
                        true
                    }
                    NodeEvent::Bookmark(_) | NodeEvent::Gone => false,
                };
                if dirty {
                    publish(&me, &nodes, &tx, &stats);
                }
            })
            .await;
        match watched {
            Ok(_) => tracing::debug!(event = "watch_ended_relisting"),
            Err(err) if err.is_gone() => tracing::info!(event = "watch_expired_relisting"),
            Err(err) => tracing::warn!(event = "watch_failed", error = %err),
        }
        if watch_started.elapsed() < KUBE_RETRY_MIN {
            tokio::time::sleep(KUBE_RETRY_MIN).await;
        }
    }
}
