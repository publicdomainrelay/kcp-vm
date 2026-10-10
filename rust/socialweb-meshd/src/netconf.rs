use std::collections::BTreeSet;
use std::net::IpAddr;

use ipnet::IpNet;
use rtnetlink::{Handle, LinkUnspec, RouteMessageBuilder};

use crate::routes::RouteTable;

pub fn tun_address(cidr: &IpNet) -> IpNet {
    let host = cidr.network();
    let max = match host {
        IpAddr::V4(_) => 32,
        IpAddr::V6(_) => 128,
    };
    IpNet::new(host, max).unwrap_or(*cidr)
}

pub fn preferred_source(table: &RouteTable, route: &IpNet) -> Option<IpAddr> {
    table
        .own_cidrs
        .iter()
        .find(|own| own.network().is_ipv4() == route.network().is_ipv4())
        .map(|own| own.network())
}

#[derive(Debug, Default, PartialEq, Eq)]
pub struct RoutePlan {
    pub add: Vec<IpNet>,
    pub remove: Vec<IpNet>,
}

pub fn plan_routes(installed: &BTreeSet<IpNet>, desired: &BTreeSet<IpNet>) -> RoutePlan {
    RoutePlan {
        add: desired.difference(installed).copied().collect(),
        remove: installed.difference(desired).copied().collect(),
    }
}

pub struct NetConf {
    handle: Handle,
    index: u32,
    addresses: BTreeSet<IpNet>,
    routes: BTreeSet<IpNet>,
}

impl NetConf {
    pub fn connect(index: u32) -> std::io::Result<NetConf> {
        let (connection, handle, _) = rtnetlink::new_connection()?;
        tokio::spawn(connection);
        Ok(NetConf {
            handle,
            index,
            addresses: BTreeSet::new(),
            routes: BTreeSet::new(),
        })
    }

    pub async fn bring_up(&self, mtu: u32) -> Result<(), rtnetlink::Error> {
        self.handle
            .link()
            .set(LinkUnspec::new_with_index(self.index).mtu(mtu).up().build())
            .execute()
            .await
    }

    pub fn installed_routes(&self) -> &BTreeSet<IpNet> {
        &self.routes
    }

    pub async fn sync(&mut self, table: &RouteTable) -> Vec<String> {
        let mut failures = Vec::new();
        let addresses_before = self.addresses.len();
        for cidr in &table.own_cidrs {
            let address = tun_address(cidr);
            if self.addresses.contains(&address) {
                continue;
            }
            let result = self
                .handle
                .address()
                .add(self.index, address.addr(), address.prefix_len())
                .replace()
                .execute()
                .await;
            match result {
                Ok(()) => {
                    self.addresses.insert(address);
                }
                Err(err) => failures.push(format!("address {address}: {err}")),
            }
        }

        let desired = table.route_cidrs();
        let mut plan = plan_routes(&self.routes, &desired);
        if self.addresses.len() != addresses_before {
            plan.add = desired.iter().copied().collect();
        }
        for cidr in plan.remove {
            let message = match self.route_message(&cidr, None) {
                Ok(message) => message,
                Err(err) => {
                    failures.push(format!("route {cidr}: {err}"));
                    continue;
                }
            };
            match self.handle.route().del(message).execute().await {
                Ok(()) => {
                    self.routes.remove(&cidr);
                }
                Err(err) => {
                    self.routes.remove(&cidr);
                    failures.push(format!("delete route {cidr}: {err}"));
                }
            }
        }
        for cidr in plan.add {
            let source = preferred_source(table, &cidr)
                .filter(|source| self.addresses.contains(&tun_address(&IpNet::from(*source))));
            let message = match self.route_message(&cidr, source) {
                Ok(message) => message,
                Err(err) => {
                    failures.push(format!("route {cidr}: {err}"));
                    continue;
                }
            };
            match self.handle.route().add(message).replace().execute().await {
                Ok(()) => {
                    self.routes.insert(cidr);
                }
                Err(err) => failures.push(format!("add route {cidr}: {err}")),
            }
        }
        failures
    }

    fn route_message(
        &self,
        cidr: &IpNet,
        source: Option<IpAddr>,
    ) -> Result<rtnetlink::packet_route::route::RouteMessage, String> {
        let mut builder = RouteMessageBuilder::<IpAddr>::new()
            .destination_prefix(cidr.network(), cidr.prefix_len())
            .map_err(|err| err.to_string())?
            .output_interface(self.index);
        if let Some(source) = source {
            builder = builder.pref_source(source).map_err(|err| err.to_string())?;
        }
        Ok(builder.build())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn net(text: &str) -> IpNet {
        text.parse().unwrap()
    }

    #[test]
    fn the_tun_takes_the_network_address_of_each_own_pod_cidr_as_a_host_address() {
        assert_eq!(tun_address(&net("10.42.1.0/24")), net("10.42.1.0/32"));
        assert_eq!(tun_address(&net("fd00:42:1::/64")), net("fd00:42:1::/128"));
    }

    #[test]
    fn a_route_sync_adds_what_is_new_and_removes_what_is_gone() {
        let installed = BTreeSet::from([net("10.42.2.0/24"), net("10.42.3.0/24")]);
        let desired = BTreeSet::from([net("10.42.3.0/24"), net("10.42.4.0/24")]);
        assert_eq!(
            plan_routes(&installed, &desired),
            RoutePlan {
                add: vec![net("10.42.4.0/24")],
                remove: vec![net("10.42.2.0/24")],
            }
        );
        assert_eq!(plan_routes(&desired, &desired), RoutePlan::default());
    }
}
