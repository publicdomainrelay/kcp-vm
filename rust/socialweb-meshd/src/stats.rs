use std::sync::atomic::{AtomicU64, Ordering};

use crate::packet::DropReason;

#[derive(Default)]
pub struct Stats {
    pub tun_read_packets: AtomicU64,
    pub tun_write_packets: AtomicU64,
    pub datagrams_sent: AtomicU64,
    pub datagrams_received: AtomicU64,
    pub bytes_sent: AtomicU64,
    pub bytes_received: AtomicU64,
    pub dropped_queue_full: AtomicU64,
    pub dropped_no_connection: AtomicU64,
    pub dropped_too_large: AtomicU64,
    pub dropped_send_failed: AtomicU64,
    pub dropped_tun_write_failed: AtomicU64,
    pub connections_dialed: AtomicU64,
    pub connections_accepted: AtomicU64,
    pub connections_refused: AtomicU64,
    pub dial_failures: AtomicU64,
    pub route_sync_failures: AtomicU64,
    pub node_list_updates: AtomicU64,
    dropped_by_reason: [AtomicU64; DropReason::ALL.len()],
}

impl Stats {
    pub fn add(counter: &AtomicU64, n: u64) {
        counter.fetch_add(n, Ordering::Relaxed);
    }

    pub fn bump(counter: &AtomicU64) {
        counter.fetch_add(1, Ordering::Relaxed);
    }

    pub fn dropped(&self, reason: DropReason) {
        Stats::bump(&self.dropped_by_reason[reason.index()]);
    }

    pub fn dropped_count(&self, reason: DropReason) -> u64 {
        self.dropped_by_reason[reason.index()].load(Ordering::Relaxed)
    }

    pub fn counters(&self) -> Vec<(String, u64)> {
        let load = |c: &AtomicU64| c.load(Ordering::Relaxed);
        let mut out = vec![
            ("tun_read_packets".to_string(), load(&self.tun_read_packets)),
            (
                "tun_write_packets".to_string(),
                load(&self.tun_write_packets),
            ),
            ("datagrams_sent".to_string(), load(&self.datagrams_sent)),
            (
                "datagrams_received".to_string(),
                load(&self.datagrams_received),
            ),
            ("bytes_sent".to_string(), load(&self.bytes_sent)),
            ("bytes_received".to_string(), load(&self.bytes_received)),
            (
                "dropped_queue_full".to_string(),
                load(&self.dropped_queue_full),
            ),
            (
                "dropped_no_connection".to_string(),
                load(&self.dropped_no_connection),
            ),
            (
                "dropped_too_large".to_string(),
                load(&self.dropped_too_large),
            ),
            (
                "dropped_send_failed".to_string(),
                load(&self.dropped_send_failed),
            ),
            (
                "dropped_tun_write_failed".to_string(),
                load(&self.dropped_tun_write_failed),
            ),
            (
                "connections_dialed".to_string(),
                load(&self.connections_dialed),
            ),
            (
                "connections_accepted".to_string(),
                load(&self.connections_accepted),
            ),
            (
                "connections_refused".to_string(),
                load(&self.connections_refused),
            ),
            ("dial_failures".to_string(), load(&self.dial_failures)),
            (
                "route_sync_failures".to_string(),
                load(&self.route_sync_failures),
            ),
            (
                "node_list_updates".to_string(),
                load(&self.node_list_updates),
            ),
        ];
        for reason in DropReason::ALL {
            out.push((
                format!("dropped_{}", reason.label()),
                self.dropped_count(reason),
            ));
        }
        out
    }

    pub fn prometheus(&self) -> String {
        let mut text = String::new();
        for (name, value) in self.counters() {
            text.push_str(&format!(
                "# TYPE socialweb_meshd_{name}_total counter\nsocialweb_meshd_{name}_total {value}\n"
            ));
        }
        text
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_drop_reason_has_its_own_counter_in_the_metrics_text() {
        let stats = Stats::default();
        stats.dropped(DropReason::UnknownDestination);
        stats.dropped(DropReason::UnknownDestination);
        stats.dropped(DropReason::UnknownPeer);
        assert_eq!(stats.dropped_count(DropReason::UnknownDestination), 2);
        let text = stats.prometheus();
        assert!(text.contains("socialweb_meshd_dropped_unknown_destination_total 2\n"));
        assert!(text.contains("socialweb_meshd_dropped_unknown_peer_total 1\n"));
        assert!(text.contains("socialweb_meshd_dropped_malformed_total 0\n"));
    }
}
