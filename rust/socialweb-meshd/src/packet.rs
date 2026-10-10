use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};

use crate::routes::{EndpointBytes, RouteTable};

pub const IPV4_HEADER_MIN: usize = 20;
pub const IPV6_HEADER_LEN: usize = 40;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub enum DropReason {
    Malformed,
    UnknownDestination,
    SourceOutsideOwnPodCidr,
    UnknownPeer,
    SourceOutsidePeerPodCidr,
    DestinationOutsideOwnPodCidr,
}

impl DropReason {
    pub const ALL: [DropReason; 6] = [
        DropReason::Malformed,
        DropReason::UnknownDestination,
        DropReason::SourceOutsideOwnPodCidr,
        DropReason::UnknownPeer,
        DropReason::SourceOutsidePeerPodCidr,
        DropReason::DestinationOutsideOwnPodCidr,
    ];

    pub fn label(self) -> &'static str {
        match self {
            DropReason::Malformed => "malformed",
            DropReason::UnknownDestination => "unknown_destination",
            DropReason::SourceOutsideOwnPodCidr => "source_outside_own_pod_cidr",
            DropReason::UnknownPeer => "unknown_peer",
            DropReason::SourceOutsidePeerPodCidr => "source_outside_peer_pod_cidr",
            DropReason::DestinationOutsideOwnPodCidr => "destination_outside_own_pod_cidr",
        }
    }

    pub fn index(self) -> usize {
        DropReason::ALL
            .iter()
            .position(|reason| *reason == self)
            .unwrap_or(0)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Addresses {
    pub src: IpAddr,
    pub dst: IpAddr,
}

pub fn addresses(packet: &[u8]) -> Option<Addresses> {
    let first = *packet.first()?;
    match first >> 4 {
        4 => {
            if packet.len() < IPV4_HEADER_MIN {
                return None;
            }
            let header_len = usize::from(first & 0x0f) * 4;
            if header_len < IPV4_HEADER_MIN || packet.len() < header_len {
                return None;
            }
            let src = Ipv4Addr::new(packet[12], packet[13], packet[14], packet[15]);
            let dst = Ipv4Addr::new(packet[16], packet[17], packet[18], packet[19]);
            Some(Addresses {
                src: IpAddr::V4(src),
                dst: IpAddr::V4(dst),
            })
        }
        6 => {
            if packet.len() < IPV6_HEADER_LEN {
                return None;
            }
            let mut src = [0u8; 16];
            let mut dst = [0u8; 16];
            src.copy_from_slice(&packet[8..24]);
            dst.copy_from_slice(&packet[24..40]);
            Some(Addresses {
                src: IpAddr::V6(Ipv6Addr::from(src)),
                dst: IpAddr::V6(Ipv6Addr::from(dst)),
            })
        }
        _ => None,
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Outbound {
    Send(EndpointBytes),
    Drop(DropReason),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Inbound {
    Deliver,
    Drop(DropReason),
}

pub fn outbound(table: &RouteTable, packet: &[u8]) -> Outbound {
    let Some(addrs) = addresses(packet) else {
        return Outbound::Drop(DropReason::Malformed);
    };
    let Some(route) = table.lookup(addrs.dst) else {
        return Outbound::Drop(DropReason::UnknownDestination);
    };
    if !table.owns(addrs.src) {
        return Outbound::Drop(DropReason::SourceOutsideOwnPodCidr);
    }
    Outbound::Send(route.endpoint)
}

pub fn inbound(table: &RouteTable, from: &EndpointBytes, packet: &[u8]) -> Inbound {
    let Some(addrs) = addresses(packet) else {
        return Inbound::Drop(DropReason::Malformed);
    };
    if !table.knows_endpoint(from) {
        return Inbound::Drop(DropReason::UnknownPeer);
    }
    if !table.endpoint_owns(from, addrs.src) {
        return Inbound::Drop(DropReason::SourceOutsidePeerPodCidr);
    }
    if !table.owns(addrs.dst) {
        return Inbound::Drop(DropReason::DestinationOutsideOwnPodCidr);
    }
    Inbound::Deliver
}

#[cfg(test)]
pub mod tests {
    use super::*;
    use crate::routes::RouteTable;
    use crate::routes::tests::{identity, node};

    pub fn ipv4_packet(src: [u8; 4], dst: [u8; 4]) -> Vec<u8> {
        let mut packet = vec![0u8; 28];
        packet[0] = 0x45;
        packet[2..4].copy_from_slice(&28u16.to_be_bytes());
        packet[8] = 64;
        packet[9] = 1;
        packet[12..16].copy_from_slice(&src);
        packet[16..20].copy_from_slice(&dst);
        packet
    }

    fn ipv6_packet(src: &str, dst: &str) -> Vec<u8> {
        let mut packet = vec![0u8; 48];
        packet[0] = 0x60;
        packet[6] = 58;
        packet[7] = 64;
        let src: Ipv6Addr = src.parse().unwrap();
        let dst: Ipv6Addr = dst.parse().unwrap();
        packet[8..24].copy_from_slice(&src.octets());
        packet[24..40].copy_from_slice(&dst.octets());
        packet
    }

    fn table() -> (RouteTable, [u8; 32], [u8; 32]) {
        let (me, b, c) = (identity(1), identity(2), identity(3));
        let nodes = vec![
            node("node-a", &["10.42.1.0/24", "fd00:42:1::/64"], Some(&me)),
            node("node-b", &["10.42.2.0/24", "fd00:42:2::/64"], Some(&b)),
            node("node-c", &["10.42.3.0/24"], Some(&c)),
        ];
        (
            RouteTable::compute("node-a", &me.public(), &nodes),
            b.public(),
            c.public(),
        )
    }

    #[test]
    fn source_and_destination_are_read_from_ipv4_and_ipv6_headers() {
        let v4 = addresses(&ipv4_packet([10, 42, 1, 5], [10, 42, 2, 6])).unwrap();
        assert_eq!(v4.src, "10.42.1.5".parse::<IpAddr>().unwrap());
        assert_eq!(v4.dst, "10.42.2.6".parse::<IpAddr>().unwrap());
        let v6 = addresses(&ipv6_packet("fd00:42:1::5", "fd00:42:2::6")).unwrap();
        assert_eq!(v6.dst, "fd00:42:2::6".parse::<IpAddr>().unwrap());
    }

    #[test]
    fn a_truncated_or_unknown_version_packet_is_malformed() {
        assert!(addresses(&[]).is_none());
        assert!(addresses(&[0x45, 0, 0]).is_none());
        assert!(addresses(&[0x46; 20]).is_none());
        assert!(addresses(&[0x60; 39]).is_none());
        assert!(addresses(&[0x20; 60]).is_none());
        let (table, _, _) = table();
        assert_eq!(
            outbound(&table, &[0x45, 0, 0]),
            Outbound::Drop(DropReason::Malformed)
        );
    }

    #[test]
    fn a_packet_to_a_peer_pod_goes_to_that_peers_endpoint() {
        let (table, b, c) = table();
        assert_eq!(
            outbound(&table, &ipv4_packet([10, 42, 1, 5], [10, 42, 2, 6])),
            Outbound::Send(b)
        );
        assert_eq!(
            outbound(&table, &ipv4_packet([10, 42, 1, 0], [10, 42, 3, 9])),
            Outbound::Send(c)
        );
        assert_eq!(
            outbound(&table, &ipv6_packet("fd00:42:1::5", "fd00:42:2::6")),
            Outbound::Send(b)
        );
    }

    #[test]
    fn a_packet_for_an_unknown_destination_is_dropped() {
        let (table, _, _) = table();
        assert_eq!(
            outbound(&table, &ipv4_packet([10, 42, 1, 5], [10, 42, 9, 6])),
            Outbound::Drop(DropReason::UnknownDestination)
        );
        assert_eq!(
            outbound(&table, &ipv4_packet([10, 42, 1, 5], [10, 42, 1, 6])),
            Outbound::Drop(DropReason::UnknownDestination)
        );
    }

    #[test]
    fn a_packet_whose_source_is_not_this_nodes_pod_cidr_is_not_sent() {
        let (table, _, _) = table();
        assert_eq!(
            outbound(&table, &ipv4_packet([10, 0, 2, 15], [10, 42, 2, 6])),
            Outbound::Drop(DropReason::SourceOutsideOwnPodCidr)
        );
    }

    #[test]
    fn a_received_packet_from_a_peer_pod_to_one_of_ours_is_delivered() {
        let (table, b, _) = table();
        assert_eq!(
            inbound(&table, &b, &ipv4_packet([10, 42, 2, 6], [10, 42, 1, 5])),
            Inbound::Deliver
        );
        assert_eq!(
            inbound(&table, &b, &ipv6_packet("fd00:42:2::6", "fd00:42:1::5")),
            Inbound::Deliver
        );
    }

    #[test]
    fn a_received_packet_spoofing_another_peers_pods_is_dropped() {
        let (table, b, _) = table();
        assert_eq!(
            inbound(&table, &b, &ipv4_packet([10, 42, 3, 6], [10, 42, 1, 5])),
            Inbound::Drop(DropReason::SourceOutsidePeerPodCidr)
        );
    }

    #[test]
    fn a_received_packet_for_somewhere_other_than_our_pods_is_dropped() {
        let (table, b, _) = table();
        assert_eq!(
            inbound(&table, &b, &ipv4_packet([10, 42, 2, 6], [10, 42, 3, 5])),
            Inbound::Drop(DropReason::DestinationOutsideOwnPodCidr)
        );
        assert_eq!(
            inbound(
                &table,
                &b,
                &ipv4_packet([10, 42, 2, 6], [169, 254, 169, 254])
            ),
            Inbound::Drop(DropReason::DestinationOutsideOwnPodCidr)
        );
    }

    #[test]
    fn a_received_packet_from_an_endpoint_with_no_route_is_dropped() {
        let (table, _, _) = table();
        let stranger = identity(9).public();
        assert_eq!(
            inbound(
                &table,
                &stranger,
                &ipv4_packet([10, 42, 2, 6], [10, 42, 1, 5])
            ),
            Inbound::Drop(DropReason::UnknownPeer)
        );
    }
}
