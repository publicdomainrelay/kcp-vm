use std::collections::BTreeSet;
use std::net::{IpAddr, SocketAddr};

use ipnet::IpNet;

use crate::identity::{endpoint_id_hex, parse_did_key, parse_endpoint_id_hex};
use crate::nodes::Node;

pub const ANNOTATION_ENDPOINT: &str = "socialweb.computer/mesh-endpoint";
pub const ANNOTATION_DID: &str = "socialweb.computer/mesh-did";
pub const ANNOTATION_ADDRS: &str = "socialweb.computer/mesh-addrs";

pub type EndpointBytes = [u8; 32];

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PeerRoute {
    pub node: String,
    pub cidr: IpNet,
    pub endpoint: EndpointBytes,
    pub addrs: Vec<SocketAddr>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Rejection {
    pub node: String,
    pub reason: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SelfView {
    pub seen: bool,
    pub endpoint_annotation: Option<String>,
    pub did_annotation: Option<String>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct RouteTable {
    pub self_node: String,
    pub own_cidrs: Vec<IpNet>,
    pub routes: Vec<PeerRoute>,
    pub rejected: Vec<Rejection>,
    pub self_view: SelfView,
}

struct Candidate {
    node: String,
    cidrs: Vec<IpNet>,
    endpoint: EndpointBytes,
    addrs: Vec<SocketAddr>,
}

fn parse_cidrs(node: &Node) -> Result<Vec<IpNet>, String> {
    let mut out = Vec::new();
    for text in node.pod_cidr_strings() {
        let net: IpNet = text
            .trim()
            .parse()
            .map_err(|err| format!("podCIDR {text:?}: {err}"))?;
        out.push(net.trunc());
    }
    Ok(out)
}

fn parse_addrs(node: &Node) -> Result<Vec<SocketAddr>, String> {
    let Some(text) = node.annotation(ANNOTATION_ADDRS) else {
        return Ok(Vec::new());
    };
    text.split(',')
        .map(str::trim)
        .filter(|part| !part.is_empty())
        .map(|part| {
            part.parse::<SocketAddr>()
                .map_err(|err| format!("{ANNOTATION_ADDRS} {part:?}: {err}"))
        })
        .collect()
}

pub fn overlaps(a: &IpNet, b: &IpNet) -> bool {
    match (a, b) {
        (IpNet::V4(_), IpNet::V4(_)) | (IpNet::V6(_), IpNet::V6(_)) => {
            a.contains(&b.network()) || b.contains(&a.network())
        }
        _ => false,
    }
}

fn candidate(node: &Node, own_endpoint: &EndpointBytes) -> Result<Option<Candidate>, String> {
    let cidrs = parse_cidrs(node)?;
    let Some(endpoint_text) = node.annotation(ANNOTATION_ENDPOINT) else {
        return Ok(None);
    };
    if cidrs.is_empty() {
        return Ok(None);
    }
    let endpoint = parse_endpoint_id_hex(endpoint_text).map_err(|err| err.to_string())?;
    if &endpoint == own_endpoint {
        return Err(format!(
            "{ANNOTATION_ENDPOINT} names this node's own endpoint {}",
            endpoint_id_hex(own_endpoint)
        ));
    }
    if let Some(did) = node.annotation(ANNOTATION_DID) {
        let did_key = parse_did_key(did).map_err(|err| err.to_string())?;
        if did_key != endpoint {
            return Err(format!(
                "{ANNOTATION_DID} {did} names a different key than {ANNOTATION_ENDPOINT} {endpoint_text}"
            ));
        }
    }
    let addrs = parse_addrs(node)?;
    Ok(Some(Candidate {
        node: node.metadata.name.clone(),
        cidrs,
        endpoint,
        addrs,
    }))
}

impl RouteTable {
    pub fn compute(self_node: &str, own_endpoint: &EndpointBytes, nodes: &[Node]) -> RouteTable {
        let mut table = RouteTable {
            self_node: self_node.to_string(),
            ..RouteTable::default()
        };
        let mut sorted: Vec<&Node> = nodes.iter().collect();
        sorted.sort_by(|a, b| a.metadata.name.cmp(&b.metadata.name));

        if let Some(own) = sorted.iter().find(|node| node.metadata.name == self_node) {
            table.self_view = SelfView {
                seen: true,
                endpoint_annotation: own.annotation(ANNOTATION_ENDPOINT).map(str::to_string),
                did_annotation: own.annotation(ANNOTATION_DID).map(str::to_string),
            };
            match parse_cidrs(own) {
                Ok(cidrs) => table.own_cidrs = cidrs,
                Err(reason) => table.rejected.push(Rejection {
                    node: self_node.to_string(),
                    reason,
                }),
            }
        }

        let mut candidates = Vec::new();
        for node in sorted.iter().filter(|node| node.metadata.name != self_node) {
            match candidate(node, own_endpoint) {
                Ok(Some(found)) => candidates.push(found),
                Ok(None) => {}
                Err(reason) => table.rejected.push(Rejection {
                    node: node.metadata.name.clone(),
                    reason,
                }),
            }
        }

        let mut refused: BTreeSet<usize> = BTreeSet::new();
        let mut reasons: Vec<Rejection> = Vec::new();
        for (index, found) in candidates.iter().enumerate() {
            for cidr in &found.cidrs {
                if let Some(own) = table.own_cidrs.iter().find(|own| overlaps(own, cidr)) {
                    refused.insert(index);
                    reasons.push(Rejection {
                        node: found.node.clone(),
                        reason: format!("podCIDR {cidr} overlaps this node's podCIDR {own}"),
                    });
                }
            }
            for (other_index, other) in candidates.iter().enumerate() {
                if other_index == index {
                    continue;
                }
                for cidr in &found.cidrs {
                    if let Some(theirs) = other.cidrs.iter().find(|theirs| overlaps(cidr, theirs)) {
                        refused.insert(index);
                        reasons.push(Rejection {
                            node: found.node.clone(),
                            reason: format!(
                                "podCIDR {cidr} overlaps node {}'s podCIDR {theirs}",
                                other.node
                            ),
                        });
                    }
                }
            }
        }
        table.rejected.extend(reasons);

        for (index, found) in candidates.into_iter().enumerate() {
            if refused.contains(&index) {
                continue;
            }
            for cidr in found.cidrs {
                table.routes.push(PeerRoute {
                    node: found.node.clone(),
                    cidr,
                    endpoint: found.endpoint,
                    addrs: found.addrs.clone(),
                });
            }
        }
        table
    }

    pub fn lookup(&self, dst: IpAddr) -> Option<&PeerRoute> {
        self.routes
            .iter()
            .filter(|route| route.cidr.contains(&dst))
            .max_by_key(|route| route.cidr.prefix_len())
    }

    pub fn owns(&self, ip: IpAddr) -> bool {
        self.own_cidrs.iter().any(|cidr| cidr.contains(&ip))
    }

    pub fn knows_endpoint(&self, endpoint: &EndpointBytes) -> bool {
        self.routes.iter().any(|route| &route.endpoint == endpoint)
    }

    pub fn endpoint_owns(&self, endpoint: &EndpointBytes, ip: IpAddr) -> bool {
        self.routes
            .iter()
            .any(|route| &route.endpoint == endpoint && route.cidr.contains(&ip))
    }

    pub fn addrs_of(&self, endpoint: &EndpointBytes) -> Vec<SocketAddr> {
        let mut out: Vec<SocketAddr> = Vec::new();
        for route in self
            .routes
            .iter()
            .filter(|route| &route.endpoint == endpoint)
        {
            for addr in &route.addrs {
                if !out.contains(addr) {
                    out.push(*addr);
                }
            }
        }
        out
    }

    pub fn endpoints(&self) -> BTreeSet<EndpointBytes> {
        self.routes.iter().map(|route| route.endpoint).collect()
    }

    pub fn route_cidrs(&self) -> BTreeSet<IpNet> {
        self.routes.iter().map(|route| route.cidr).collect()
    }

    pub fn self_annotated_with(&self, endpoint_hex: &str, did: &str) -> bool {
        self.self_view.seen
            && self.self_view.endpoint_annotation.as_deref() == Some(endpoint_hex)
            && self.self_view.did_annotation.as_deref() == Some(did)
    }
}

#[cfg(test)]
pub mod tests {
    use super::*;
    use crate::identity::{Identity, did_key};
    use std::collections::BTreeMap;

    pub fn identity(seed_byte: u8) -> Identity {
        Identity::from_seed(&[seed_byte; 32])
    }

    pub fn node(name: &str, cidrs: &[&str], endpoint: Option<&Identity>) -> Node {
        let mut annotations = BTreeMap::new();
        if let Some(identity) = endpoint {
            annotations.insert(ANNOTATION_ENDPOINT.to_string(), identity.endpoint_id_hex());
            annotations.insert(ANNOTATION_DID.to_string(), identity.did_key());
        }
        Node {
            metadata: crate::nodes::ObjectMeta {
                name: name.to_string(),
                annotations,
                resource_version: None,
            },
            spec: crate::nodes::NodeSpec {
                pod_cidr: cidrs.first().map(|c| c.to_string()),
                pod_cidrs: cidrs.iter().map(|c| c.to_string()).collect(),
            },
        }
    }

    fn net(text: &str) -> IpNet {
        text.parse().unwrap()
    }

    fn ip(text: &str) -> IpAddr {
        text.parse().unwrap()
    }

    #[test]
    fn every_other_annotated_node_with_a_pod_cidr_becomes_one_route_to_its_endpoint() {
        let (me, b, c) = (identity(1), identity(2), identity(3));
        let nodes = vec![
            node("node-c", &["10.42.3.0/24"], Some(&c)),
            node("node-a", &["10.42.1.0/24"], Some(&me)),
            node("node-b", &["10.42.2.0/24"], Some(&b)),
        ];
        let table = RouteTable::compute("node-a", &me.public(), &nodes);
        assert_eq!(table.own_cidrs, vec![net("10.42.1.0/24")]);
        assert_eq!(
            table.routes,
            vec![
                PeerRoute {
                    node: "node-b".into(),
                    cidr: net("10.42.2.0/24"),
                    endpoint: b.public(),
                    addrs: vec![]
                },
                PeerRoute {
                    node: "node-c".into(),
                    cidr: net("10.42.3.0/24"),
                    endpoint: c.public(),
                    addrs: vec![]
                },
            ]
        );
        assert!(table.rejected.is_empty());
        assert!(table.self_annotated_with(&me.endpoint_id_hex(), &me.did_key()));
    }

    #[test]
    fn a_node_without_a_pod_cidr_or_without_an_endpoint_is_skipped_silently() {
        let (me, b) = (identity(1), identity(2));
        let nodes = vec![
            node("node-a", &["10.42.1.0/24"], Some(&me)),
            node("node-b", &[], Some(&b)),
            node("node-c", &["10.42.3.0/24"], None),
        ];
        let table = RouteTable::compute("node-a", &me.public(), &nodes);
        assert!(table.routes.is_empty());
        assert!(table.rejected.is_empty());
    }

    #[test]
    fn this_nodes_own_pod_cidr_is_never_routed_even_when_this_node_is_unannotated() {
        let (me, b) = (identity(1), identity(2));
        let nodes = vec![
            node("node-a", &["10.42.1.0/24"], None),
            node("node-b", &["10.42.2.0/24"], Some(&b)),
        ];
        let table = RouteTable::compute("node-a", &me.public(), &nodes);
        assert_eq!(table.route_cidrs(), BTreeSet::from([net("10.42.2.0/24")]));
        assert!(table.owns(ip("10.42.1.7")));
        assert!(!table.self_annotated_with(&me.endpoint_id_hex(), &me.did_key()));
        assert!(table.self_view.seen);
    }

    #[test]
    fn a_peer_overlapping_this_nodes_pod_cidr_is_rejected() {
        let (me, b) = (identity(1), identity(2));
        let nodes = vec![
            node("node-a", &["10.42.1.0/24"], Some(&me)),
            node("node-b", &["10.42.0.0/16"], Some(&b)),
        ];
        let table = RouteTable::compute("node-a", &me.public(), &nodes);
        assert!(table.routes.is_empty());
        assert_eq!(table.rejected.len(), 1);
        assert_eq!(table.rejected[0].node, "node-b");
    }

    #[test]
    fn two_peers_claiming_overlapping_pod_cidrs_are_both_rejected() {
        let (me, b, c, d) = (identity(1), identity(2), identity(3), identity(4));
        let nodes = vec![
            node("node-a", &["10.42.1.0/24"], Some(&me)),
            node("node-b", &["10.42.2.0/24"], Some(&b)),
            node("node-c", &["10.42.2.128/25"], Some(&c)),
            node("node-d", &["10.42.4.0/24"], Some(&d)),
        ];
        let table = RouteTable::compute("node-a", &me.public(), &nodes);
        assert_eq!(table.route_cidrs(), BTreeSet::from([net("10.42.4.0/24")]));
        let rejected: BTreeSet<&str> = table.rejected.iter().map(|r| r.node.as_str()).collect();
        assert_eq!(rejected, BTreeSet::from(["node-b", "node-c"]));
    }

    #[test]
    fn a_peer_whose_did_names_a_different_key_than_its_endpoint_is_rejected() {
        let (me, b, c) = (identity(1), identity(2), identity(3));
        let mut forged = node("node-b", &["10.42.2.0/24"], Some(&b));
        forged
            .metadata
            .annotations
            .insert(ANNOTATION_DID.to_string(), did_key(&c.public()));
        let nodes = vec![node("node-a", &["10.42.1.0/24"], Some(&me)), forged];
        let table = RouteTable::compute("node-a", &me.public(), &nodes);
        assert!(table.routes.is_empty());
        assert_eq!(table.rejected.len(), 1);
    }

    #[test]
    fn a_peer_claiming_this_nodes_own_endpoint_or_a_malformed_one_is_rejected() {
        let me = identity(1);
        let mut garbage = node("node-c", &["10.42.3.0/24"], None);
        garbage
            .metadata
            .annotations
            .insert(ANNOTATION_ENDPOINT.to_string(), "zz".to_string());
        let nodes = vec![
            node("node-a", &["10.42.1.0/24"], Some(&me)),
            node("node-b", &["10.42.2.0/24"], Some(&me)),
            garbage,
        ];
        let table = RouteTable::compute("node-a", &me.public(), &nodes);
        assert!(table.routes.is_empty());
        let rejected: BTreeSet<&str> = table.rejected.iter().map(|r| r.node.as_str()).collect();
        assert_eq!(rejected, BTreeSet::from(["node-b", "node-c"]));
    }

    #[test]
    fn a_dual_stack_peer_gets_one_route_per_pod_cidr_and_lookup_takes_the_longest_prefix() {
        let (me, b, c) = (identity(1), identity(2), identity(3));
        let nodes = vec![
            node("node-a", &["10.42.1.0/24"], Some(&me)),
            node("node-b", &["10.42.2.0/24", "fd00:42:2::/64"], Some(&b)),
            node("node-c", &["10.43.0.0/16"], Some(&c)),
        ];
        let table = RouteTable::compute("node-a", &me.public(), &nodes);
        assert_eq!(table.routes.len(), 3);
        assert_eq!(table.lookup(ip("fd00:42:2::9")).unwrap().node, "node-b");
        assert_eq!(table.lookup(ip("10.42.2.9")).unwrap().node, "node-b");
        assert_eq!(table.lookup(ip("10.43.200.1")).unwrap().node, "node-c");
        assert!(table.lookup(ip("10.42.1.9")).is_none());
        assert!(table.lookup(ip("8.8.8.8")).is_none());
        assert!(table.endpoint_owns(&b.public(), ip("fd00:42:2::9")));
        assert!(!table.endpoint_owns(&c.public(), ip("10.42.2.9")));
    }

    #[test]
    fn a_pod_cidr_with_host_bits_set_is_normalised_to_its_network() {
        let (me, b) = (identity(1), identity(2));
        let nodes = vec![
            node("node-a", &["10.42.1.0/24"], Some(&me)),
            node("node-b", &["10.42.2.77/24"], Some(&b)),
        ];
        let table = RouteTable::compute("node-a", &me.public(), &nodes);
        assert_eq!(table.route_cidrs(), BTreeSet::from([net("10.42.2.0/24")]));
    }

    #[test]
    fn the_direct_address_hint_annotation_is_carried_on_the_route() {
        let (me, b) = (identity(1), identity(2));
        let mut peer = node("node-b", &["10.42.2.0/24"], Some(&b));
        peer.metadata.annotations.insert(
            ANNOTATION_ADDRS.to_string(),
            "10.199.0.2:47001, [fd00::2]:47001".to_string(),
        );
        let nodes = vec![node("node-a", &["10.42.1.0/24"], Some(&me)), peer];
        let table = RouteTable::compute("node-a", &me.public(), &nodes);
        assert_eq!(
            table.addrs_of(&b.public()),
            vec![
                "10.199.0.2:47001".parse::<SocketAddr>().unwrap(),
                "[fd00::2]:47001".parse().unwrap()
            ]
        );
    }

    #[test]
    fn a_self_node_absent_from_the_list_leaves_no_own_cidr_and_is_reported_unseen() {
        let (me, b) = (identity(1), identity(2));
        let nodes = vec![node("node-b", &["10.42.2.0/24"], Some(&b))];
        let table = RouteTable::compute("node-a", &me.public(), &nodes);
        assert!(!table.self_view.seen);
        assert!(table.own_cidrs.is_empty());
        assert_eq!(table.routes.len(), 1);
    }
}
