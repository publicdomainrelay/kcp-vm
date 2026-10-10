use std::collections::BTreeMap;

use serde::Deserialize;

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub struct ObjectMeta {
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub annotations: BTreeMap<String, String>,
    #[serde(default)]
    pub resource_version: Option<String>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct NodeSpec {
    #[serde(rename = "podCIDR", default)]
    pub pod_cidr: Option<String>,
    #[serde(rename = "podCIDRs", default)]
    pub pod_cidrs: Vec<String>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct Node {
    #[serde(default)]
    pub metadata: ObjectMeta,
    #[serde(default)]
    pub spec: NodeSpec,
}

impl Node {
    pub fn pod_cidr_strings(&self) -> Vec<String> {
        if !self.spec.pod_cidrs.is_empty() {
            return self.spec.pod_cidrs.clone();
        }
        self.spec.pod_cidr.iter().cloned().collect()
    }

    pub fn annotation(&self, key: &str) -> Option<&str> {
        self.metadata.annotations.get(key).map(String::as_str)
    }
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ListMeta {
    #[serde(default)]
    pub resource_version: Option<String>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct NodeList {
    #[serde(default)]
    pub metadata: ListMeta,
    #[serde(default)]
    pub items: Vec<Node>,
}

#[derive(Debug, Deserialize)]
pub struct WatchEvent {
    #[serde(rename = "type")]
    pub kind: String,
    pub object: serde_json::Value,
}

#[derive(Clone, Debug, Default)]
pub struct NodeSet {
    nodes: BTreeMap<String, Node>,
}

impl NodeSet {
    pub fn from_list(list: NodeList) -> NodeSet {
        let mut set = NodeSet::default();
        for node in list.items {
            set.upsert(node);
        }
        set
    }

    pub fn upsert(&mut self, node: Node) {
        self.nodes.insert(node.metadata.name.clone(), node);
    }

    pub fn remove(&mut self, name: &str) {
        self.nodes.remove(name);
    }

    pub fn get(&self, name: &str) -> Option<&Node> {
        self.nodes.get(name)
    }

    pub fn to_vec(&self) -> Vec<Node> {
        self.nodes.values().cloned().collect()
    }

    pub fn len(&self) -> usize {
        self.nodes.len()
    }

    pub fn is_empty(&self) -> bool {
        self.nodes.is_empty()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_kubectl_node_list_parses_with_pod_cidrs_and_annotations() {
        let json = r#"{
          "apiVersion": "v1", "kind": "NodeList",
          "metadata": {"resourceVersion": "42"},
          "items": [
            {"metadata": {"name": "a", "resourceVersion": "7",
                          "annotations": {"socialweb.computer/mesh-endpoint": "ab"}},
             "spec": {"podCIDR": "10.42.1.0/24", "podCIDRs": ["10.42.1.0/24", "fd00:42:1::/64"]}},
            {"metadata": {"name": "b"}, "spec": {"podCIDR": "10.42.2.0/24"}},
            {"metadata": {"name": "c"}, "spec": {}}
          ]}"#;
        let list: NodeList = serde_json::from_str(json).unwrap();
        assert_eq!(list.metadata.resource_version.as_deref(), Some("42"));
        let set = NodeSet::from_list(list);
        assert_eq!(set.len(), 3);
        let a = set.get("a").unwrap();
        assert_eq!(a.pod_cidr_strings(), vec!["10.42.1.0/24", "fd00:42:1::/64"]);
        assert_eq!(a.annotation("socialweb.computer/mesh-endpoint"), Some("ab"));
        assert_eq!(
            set.get("b").unwrap().pod_cidr_strings(),
            vec!["10.42.2.0/24"]
        );
        assert!(set.get("c").unwrap().pod_cidr_strings().is_empty());
    }

    #[test]
    fn a_watch_event_line_parses() {
        let line = r#"{"type":"MODIFIED","object":{"kind":"Node","metadata":{"name":"a"}}}"#;
        let event: WatchEvent = serde_json::from_str(line).unwrap();
        assert_eq!(event.kind, "MODIFIED");
        let node: Node = serde_json::from_value(event.object).unwrap();
        assert_eq!(node.metadata.name, "a");
    }
}
