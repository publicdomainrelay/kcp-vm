use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use bytes::Bytes;
use iroh::endpoint::{Connection, SendDatagramError, presets};
use iroh::{Endpoint, EndpointAddr, EndpointId, RelayMode, RelayUrl, TransportAddr};
use tokio::sync::{mpsc, watch};
use tokio::time::Instant;

use crate::identity::endpoint_id_hex;
use crate::packet::{self, Inbound, Outbound};
use crate::routes::{EndpointBytes, RouteTable};
use crate::stats::Stats;
use crate::tun::Tun;

pub const ALPN: &[u8] = b"socialweb.computer/meshd/0";
pub const PEER_QUEUE_PACKETS: usize = 512;
pub const DIAL_TIMEOUT: Duration = Duration::from_secs(15);
pub const DIAL_BACKOFF_MIN: Duration = Duration::from_secs(1);
pub const DIAL_BACKOFF_MAX: Duration = Duration::from_secs(30);
pub const TUN_READ_BUFFER: usize = 65536;
pub const CLOSE_UNKNOWN_PEER: u32 = 1;
pub const CLOSE_SHUTDOWN: u32 = 0;

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum RelayChoice {
    Default,
    Disabled,
    Custom(Vec<RelayUrl>),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DiscoveryChoice {
    N0,
    None,
}

#[derive(Clone, Debug)]
pub struct EndpointOptions {
    pub relay: RelayChoice,
    pub discovery: DiscoveryChoice,
    pub bind: Vec<SocketAddr>,
}

pub async fn bind_endpoint(
    secret: iroh::SecretKey,
    options: &EndpointOptions,
) -> Result<Endpoint, String> {
    let mut builder = match options.discovery {
        DiscoveryChoice::N0 => Endpoint::builder(presets::N0),
        DiscoveryChoice::None => Endpoint::builder(presets::Minimal),
    };
    builder = builder
        .secret_key(secret)
        .alpns(vec![ALPN.to_vec()])
        .relay_mode(match &options.relay {
            RelayChoice::Default => RelayMode::Default,
            RelayChoice::Disabled => RelayMode::Disabled,
            RelayChoice::Custom(urls) => RelayMode::custom(urls.clone()),
        });
    if !options.bind.is_empty() {
        builder = builder.clear_ip_transports();
        for addr in &options.bind {
            builder = builder
                .bind_addr(*addr)
                .map_err(|err| format!("bind address {addr}: {err}"))?;
        }
    }
    builder.bind().await.map_err(|err| err.to_string())
}

pub fn endpoint_id(bytes: &EndpointBytes) -> Option<EndpointId> {
    EndpointId::from_bytes(bytes).ok()
}

#[derive(Default)]
pub struct Connections {
    by_peer: Mutex<HashMap<EndpointBytes, Connection>>,
}

impl Connections {
    pub fn live(&self, peer: &EndpointBytes) -> Option<Connection> {
        let map = self.by_peer.lock().unwrap_or_else(|p| p.into_inner());
        map.get(peer)
            .filter(|conn| conn.close_reason().is_none())
            .cloned()
    }

    pub fn insert(&self, peer: EndpointBytes, conn: Connection) {
        let mut map = self.by_peer.lock().unwrap_or_else(|p| p.into_inner());
        map.insert(peer, conn);
    }

    pub fn forget(&self, peer: &EndpointBytes, stable_id: usize) {
        let mut map = self.by_peer.lock().unwrap_or_else(|p| p.into_inner());
        if map
            .get(peer)
            .is_some_and(|conn| conn.stable_id() == stable_id)
        {
            map.remove(peer);
        }
    }

    pub fn retain_known(&self, table: &RouteTable) {
        let mut map = self.by_peer.lock().unwrap_or_else(|p| p.into_inner());
        map.retain(|peer, conn| {
            let keep = table.knows_endpoint(peer);
            if !keep {
                conn.close(CLOSE_UNKNOWN_PEER.into(), b"peer left the node list");
            }
            keep
        });
    }

    pub fn connected(&self) -> Vec<(EndpointBytes, Option<usize>)> {
        let map = self.by_peer.lock().unwrap_or_else(|p| p.into_inner());
        map.iter()
            .filter(|(_, conn)| conn.close_reason().is_none())
            .map(|(peer, conn)| (*peer, conn.max_datagram_size()))
            .collect()
    }

    pub fn close_all(&self) {
        let map = self.by_peer.lock().unwrap_or_else(|p| p.into_inner());
        for conn in map.values() {
            conn.close(CLOSE_SHUTDOWN.into(), b"shutdown");
        }
    }
}

pub struct Mesh {
    pub endpoint: Endpoint,
    pub tun: Arc<Tun>,
    pub routes: watch::Receiver<Arc<RouteTable>>,
    pub connections: Arc<Connections>,
    pub stats: Arc<Stats>,
}

impl Mesh {
    pub fn spawn_receiver(self: &Arc<Self>, peer: EndpointBytes, conn: Connection) {
        let mesh = Arc::clone(self);
        tokio::spawn(async move {
            let stable_id = conn.stable_id();
            let reason = loop {
                match conn.read_datagram().await {
                    Ok(datagram) => {
                        Stats::bump(&mesh.stats.datagrams_received);
                        Stats::add(&mesh.stats.bytes_received, datagram.len() as u64);
                        let verdict = {
                            let table = mesh.routes.borrow();
                            packet::inbound(&table, &peer, &datagram)
                        };
                        match verdict {
                            Inbound::Deliver => match mesh.tun.send(&datagram).await {
                                Ok(_) => Stats::bump(&mesh.stats.tun_write_packets),
                                Err(err) => {
                                    Stats::bump(&mesh.stats.dropped_tun_write_failed);
                                    tracing::debug!(event = "tun_write_failed", error = %err);
                                }
                            },
                            Inbound::Drop(reason) => mesh.stats.dropped(reason),
                        }
                    }
                    Err(err) => break err,
                }
            };
            mesh.connections.forget(&peer, stable_id);
            tracing::info!(
                event = "connection_closed",
                peer = %endpoint_id_hex(&peer),
                reason = %reason
            );
        });
    }

    pub async fn run_accept(self: Arc<Self>) {
        while let Some(incoming) = self.endpoint.accept().await {
            let mesh = Arc::clone(&self);
            tokio::spawn(async move {
                let accepting = match incoming.accept() {
                    Ok(accepting) => accepting,
                    Err(err) => {
                        tracing::debug!(event = "incoming_failed", error = %err);
                        return;
                    }
                };
                let conn = match accepting.await {
                    Ok(conn) => conn,
                    Err(err) => {
                        tracing::debug!(event = "handshake_failed", error = %err);
                        return;
                    }
                };
                let peer = *conn.remote_id().as_bytes();
                if !mesh.routes.borrow().knows_endpoint(&peer) {
                    Stats::bump(&mesh.stats.connections_refused);
                    tracing::warn!(event = "connection_refused", peer = %endpoint_id_hex(&peer), reason = "no node routes to this endpoint");
                    conn.close(CLOSE_UNKNOWN_PEER.into(), b"unknown peer");
                    return;
                }
                Stats::bump(&mesh.stats.connections_accepted);
                tracing::info!(
                    event = "connection_accepted",
                    peer = %endpoint_id_hex(&peer),
                    max_datagram_size = ?conn.max_datagram_size()
                );
                mesh.connections.insert(peer, conn.clone());
                mesh.spawn_receiver(peer, conn);
            });
        }
    }

    pub async fn run_outbound(self: Arc<Self>) -> std::io::Result<()> {
        let mut buf = vec![0u8; TUN_READ_BUFFER];
        let mut senders: HashMap<EndpointBytes, mpsc::Sender<Bytes>> = HashMap::new();
        let mut routes = self.routes.clone();
        loop {
            tokio::select! {
                changed = routes.changed() => {
                    if changed.is_err() {
                        return Ok(());
                    }
                    let table = Arc::clone(&routes.borrow_and_update());
                    senders.retain(|peer, _| table.knows_endpoint(peer));
                    self.connections.retain_known(&table);
                }
                read = self.tun.recv(&mut buf) => {
                    let n = read?;
                    Stats::bump(&self.stats.tun_read_packets);
                    let packet = &buf[..n];
                    let verdict = {
                        let table = routes.borrow();
                        packet::outbound(&table, packet)
                    };
                    let peer = match verdict {
                        Outbound::Send(peer) => peer,
                        Outbound::Drop(reason) => {
                            self.stats.dropped(reason);
                            continue;
                        }
                    };
                    let sender = senders
                        .entry(peer)
                        .or_insert_with(|| self.spawn_peer(peer));
                    if sender.try_send(Bytes::copy_from_slice(packet)).is_err() {
                        Stats::bump(&self.stats.dropped_queue_full);
                    }
                }
            }
        }
    }

    fn spawn_peer(self: &Arc<Self>, peer: EndpointBytes) -> mpsc::Sender<Bytes> {
        let (tx, rx) = mpsc::channel(PEER_QUEUE_PACKETS);
        let mesh = Arc::clone(self);
        tokio::spawn(async move { mesh.run_peer(peer, rx).await });
        tx
    }

    fn peer_addr(&self, peer: &EndpointBytes) -> Option<EndpointAddr> {
        let id = endpoint_id(peer)?;
        let addrs = self.routes.borrow().addrs_of(peer);
        Some(EndpointAddr::from_parts(
            id,
            addrs.into_iter().map(TransportAddr::Ip),
        ))
    }

    async fn dial(self: &Arc<Self>, peer: &EndpointBytes) -> Result<Connection, String> {
        let addr = self
            .peer_addr(peer)
            .ok_or_else(|| "endpoint id is not a valid key".to_string())?;
        let conn = tokio::time::timeout(DIAL_TIMEOUT, self.endpoint.connect(addr, ALPN))
            .await
            .map_err(|_| format!("no connection within {DIAL_TIMEOUT:?}"))?
            .map_err(|err| err.to_string())?;
        Stats::bump(&self.stats.connections_dialed);
        tracing::info!(
            event = "connection_dialed",
            peer = %endpoint_id_hex(peer),
            max_datagram_size = ?conn.max_datagram_size()
        );
        self.connections.insert(*peer, conn.clone());
        self.spawn_receiver(*peer, conn.clone());
        Ok(conn)
    }

    async fn run_peer(self: Arc<Self>, peer: EndpointBytes, mut rx: mpsc::Receiver<Bytes>) {
        let mut next_dial = Instant::now();
        let mut backoff = DIAL_BACKOFF_MIN;
        while let Some(datagram) = rx.recv().await {
            let conn = match self.connections.live(&peer) {
                Some(conn) => conn,
                None => {
                    if Instant::now() < next_dial {
                        Stats::bump(&self.stats.dropped_no_connection);
                        continue;
                    }
                    match self.dial(&peer).await {
                        Ok(conn) => {
                            backoff = DIAL_BACKOFF_MIN;
                            conn
                        }
                        Err(err) => {
                            Stats::bump(&self.stats.dial_failures);
                            Stats::bump(&self.stats.dropped_no_connection);
                            tracing::warn!(
                                event = "dial_failed",
                                peer = %endpoint_id_hex(&peer),
                                error = %err,
                                retry_in_ms = backoff.as_millis() as u64
                            );
                            next_dial = Instant::now() + backoff;
                            backoff = (backoff * 2).min(DIAL_BACKOFF_MAX);
                            continue;
                        }
                    }
                }
            };
            let len = datagram.len() as u64;
            match conn.send_datagram(datagram) {
                Ok(()) => {
                    Stats::bump(&self.stats.datagrams_sent);
                    Stats::add(&self.stats.bytes_sent, len);
                }
                Err(SendDatagramError::TooLarge) => {
                    Stats::bump(&self.stats.dropped_too_large);
                    tracing::debug!(
                        event = "datagram_too_large",
                        len,
                        max_datagram_size = ?conn.max_datagram_size()
                    );
                }
                Err(err) => {
                    Stats::bump(&self.stats.dropped_send_failed);
                    self.connections.forget(&peer, conn.stable_id());
                    tracing::info!(event = "send_failed", peer = %endpoint_id_hex(&peer), error = %err);
                }
            }
        }
    }
}
