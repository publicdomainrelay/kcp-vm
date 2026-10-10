mod common;

use std::fs::File;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::time::Duration;

use common::{
    READY_WAIT, eventually, gate, identity, must, reap, scratch_dir, status,
    stop_namespace_processes, sudo, wait_exit, wait_for_log,
};

const GATE: &str = "SOCIALWEB_MESHD_K3S";
const K3S_READY_WAIT: Duration = Duration::from_secs(180);
const APISERVER_PORT: &str = "16443";

struct Rig {
    ns: String,
    dir: PathBuf,
    children: Vec<Child>,
}

impl Drop for Rig {
    fn drop(&mut self) {
        stop_namespace_processes(&self.ns);
        for child in &mut self.children {
            reap(child, Duration::from_secs(20));
        }
        let _ = sudo(&["ip", "netns", "del", &self.ns]);
        let _ = sudo(&["rm", "-rf", self.dir.to_str().unwrap()]);
    }
}

fn kubectl(rig: &Rig, args: &[&str]) -> std::process::Output {
    let admin = rig.dir.join("admin.yaml");
    let mut full = vec![
        "ip",
        "netns",
        "exec",
        &rig.ns,
        "k3s",
        "kubectl",
        "--kubeconfig",
        admin.to_str().unwrap(),
    ];
    full.extend_from_slice(args);
    sudo(&full)
}

fn kubectl_ok(rig: &Rig, args: &[&str]) -> String {
    let out = kubectl(rig, args);
    assert!(
        out.status.success(),
        "kubectl {args:?}: {}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    String::from_utf8_lossy(&out.stdout).into_owned()
}

fn node_manifest(name: &str, cidr: &str, mesh: Option<(&str, &str)>) -> serde_json::Value {
    let mut annotations = serde_json::Map::new();
    if let Some((endpoint, did)) = mesh {
        annotations.insert(
            "socialweb.computer/mesh-endpoint".to_string(),
            endpoint.into(),
        );
        annotations.insert("socialweb.computer/mesh-did".to_string(), did.into());
    }
    serde_json::json!({
        "apiVersion": "v1",
        "kind": "Node",
        "metadata": {"name": name, "annotations": annotations},
        "spec": {"podCIDR": cidr, "podCIDRs": [cidr]}
    })
}

fn apply(rig: &Rig, file: &str, manifest: &serde_json::Value) {
    let path = rig.dir.join(file);
    std::fs::write(&path, manifest.to_string()).unwrap();
    kubectl_ok(rig, &["apply", "-f", path.to_str().unwrap()]);
}

fn mint_node_credentials(rig: &Rig, node: &str) -> PathBuf {
    let tls = rig.dir.join("data/server/tls");
    let key_pkcs8 = rig.dir.join(format!("{node}.pkcs8.key"));
    let key = rig.dir.join(format!("{node}.key"));
    let csr = rig.dir.join(format!("{node}.csr"));
    let crt = rig.dir.join(format!("{node}.crt"));
    must(&[
        "openssl",
        "req",
        "-new",
        "-newkey",
        "ec",
        "-pkeyopt",
        "ec_paramgen_curve:prime256v1",
        "-nodes",
        "-keyout",
        key_pkcs8.to_str().unwrap(),
        "-out",
        csr.to_str().unwrap(),
        "-subj",
        &format!("/O=system:nodes/CN=system:node:{node}"),
    ]);
    must(&[
        "openssl",
        "ec",
        "-in",
        key_pkcs8.to_str().unwrap(),
        "-out",
        key.to_str().unwrap(),
    ]);
    must(&[
        "openssl",
        "x509",
        "-req",
        "-in",
        csr.to_str().unwrap(),
        "-CA",
        tls.join("client-ca.crt").to_str().unwrap(),
        "-CAkey",
        tls.join("client-ca.key").to_str().unwrap(),
        "-CAcreateserial",
        "-days",
        "1",
        "-out",
        crt.to_str().unwrap(),
    ]);
    let kubeconfig = rig.dir.join(format!("{node}.kubeconfig"));
    let text = format!(
        "apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    server: https://127.0.0.1:{APISERVER_PORT}\n    certificate-authority: data/server/tls/server-ca.crt\n  name: local\ncontexts:\n- context:\n    cluster: local\n    namespace: default\n    user: user\n  name: Default\ncurrent-context: Default\npreferences: {{}}\nusers:\n- name: user\n  user:\n    client-certificate: {node}.crt\n    client-key: {node}.key\n"
    );
    std::fs::write(&kubeconfig, text).unwrap();
    kubeconfig
}

fn start_meshd(
    rig: &mut Rig,
    bin: &str,
    node: &str,
    kubeconfig: &Path,
    secret: &Path,
    log: &Path,
) -> usize {
    let file = File::create(log).unwrap();
    let child = Command::new("sudo")
        .args(["-n", "ip", "netns", "exec", &rig.ns, bin, "run"])
        .args(["--node-name", node])
        .arg("--kubeconfig")
        .arg(kubeconfig)
        .arg("--secret-file")
        .arg(secret)
        .args(["--relay", "disabled", "--discovery", "none"])
        .args(["--metrics-listen", "127.0.0.1:9469"])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::from(file))
        .spawn()
        .expect("spawn meshd");
    rig.children.push(child);
    rig.children.len() - 1
}

fn route_nodes(ns: &str) -> Vec<String> {
    status(ns)["routes"]
        .as_array()
        .unwrap()
        .iter()
        .map(|r| r["node"].as_str().unwrap().to_string())
        .collect()
}

fn installed(ns: &str) -> String {
    must(&["ip", "-n", ns, "route", "show", "dev", "swmesh0"])
}

#[test]
fn with_a_kubelet_identity_meshd_annotates_its_own_node_and_follows_the_node_list() {
    if !gate(GATE) {
        return;
    }
    let bin_path = common::meshd_bin();
    let bin = bin_path.as_str();
    let dir = scratch_dir("k3s");
    let mut rig = Rig {
        ns: format!("swmesh-{}-k3s", std::process::id()),
        dir,
        children: Vec::new(),
    };
    must(&["ip", "netns", "add", &rig.ns]);
    must(&["ip", "-n", &rig.ns, "link", "set", "lo", "up"]);
    must(&[
        "ip", "-n", &rig.ns, "link", "add", "uplink0", "type", "dummy",
    ]);
    must(&[
        "ip",
        "-n",
        &rig.ns,
        "addr",
        "add",
        "10.250.0.2/24",
        "dev",
        "uplink0",
    ]);
    must(&["ip", "-n", &rig.ns, "link", "set", "uplink0", "up"]);
    must(&[
        "ip",
        "-n",
        &rig.ns,
        "route",
        "add",
        "default",
        "via",
        "10.250.0.1",
    ]);

    let data = rig.dir.join("data");
    let admin = rig.dir.join("admin.yaml");
    let k3s_log = rig.dir.join("k3s.log");
    let k3s = Command::new("sudo")
        .args(["-n", "ip", "netns", "exec", &rig.ns, "k3s", "server"])
        .args(["--disable-agent", "--disable-cloud-controller"])
        .args([
            "--disable",
            "traefik,servicelb,metrics-server,local-storage,coredns",
        ])
        .arg("--data-dir")
        .arg(&data)
        .args([
            "--bind-address",
            "127.0.0.1",
            "--advertise-address",
            "127.0.0.1",
        ])
        .args(["--https-listen-port", APISERVER_PORT])
        .arg("--write-kubeconfig")
        .arg(&admin)
        .args(["--write-kubeconfig-mode", "0644"])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::from(File::create(&k3s_log).unwrap()))
        .spawn()
        .expect("spawn k3s");
    rig.children.push(k3s);
    eventually(K3S_READY_WAIT, "k3s apiserver /readyz", || {
        kubectl(&rig, &["get", "--raw", "/readyz"]).status.success()
    });

    let secret_a = rig.dir.join("a.secret");
    let (endpoint_a, did_a) = identity(bin, &secret_a);
    let (endpoint_b, did_b) = identity(bin, &rig.dir.join("b.secret"));
    let (endpoint_c, did_c) = identity(bin, &rig.dir.join("c.secret"));
    apply(
        &rig,
        "node-a.json",
        &node_manifest("node-a", "10.42.1.0/24", None),
    );
    apply(
        &rig,
        "node-b.json",
        &node_manifest("node-b", "10.42.2.0/24", Some((&endpoint_b, &did_b))),
    );
    let kubeconfig_a = mint_node_credentials(&rig, "node-a");

    let log_a = rig.dir.join("meshd-a.log");
    let a = start_meshd(&mut rig, bin, "node-a", &kubeconfig_a, &secret_a, &log_a);
    let before_rbac = wait_for_log(
        &mut rig.children[a],
        &log_a,
        "\"event\":\"list_nodes_forbidden\"",
        READY_WAIT,
    );
    assert!(
        before_rbac.contains("cannot read all nodes, only its own Node object"),
        "{before_rbac}"
    );
    assert_eq!(status(&rig.ns)["ready"], false);
    let rbac = Path::new(env!("CARGO_MANIFEST_DIR")).join("deploy/node-reader-rbac.yaml");
    kubectl_ok(&rig, &["apply", "-f", rbac.to_str().unwrap()]);
    wait_for_log(
        &mut rig.children[a],
        &log_a,
        "\"event\":\"ready\"",
        READY_WAIT,
    );

    let annotations = kubectl_ok(
        &rig,
        &[
            "get",
            "node",
            "node-a",
            "-o",
            "jsonpath={.metadata.annotations}",
        ],
    );
    let annotations: serde_json::Value = serde_json::from_str(&annotations).unwrap();
    assert_eq!(annotations["socialweb.computer/mesh-endpoint"], endpoint_a);
    assert_eq!(annotations["socialweb.computer/mesh-did"], did_a);
    assert_eq!(route_nodes(&rig.ns), vec!["node-b"]);
    assert!(installed(&rig.ns).contains("10.42.2.0/24"));

    apply(
        &rig,
        "node-c.json",
        &node_manifest("node-c", "10.42.3.0/24", Some((&endpoint_c, &did_c))),
    );
    eventually(Duration::from_secs(15), "watch adds node-c", || {
        route_nodes(&rig.ns) == vec!["node-b", "node-c"]
            && installed(&rig.ns).contains("10.42.3.0/24")
    });
    kubectl_ok(&rig, &["delete", "node", "node-b", "--wait=true"]);
    eventually(Duration::from_secs(15), "watch drops node-b", || {
        route_nodes(&rig.ns) == vec!["node-c"] && !installed(&rig.ns).contains("10.42.2.0/24")
    });

    stop_namespace_processes_named(&rig, "socialweb-meshd");
    assert_eq!(wait_exit(&mut rig.children[a], Duration::from_secs(10)), 0);

    let log_wrong = rig.dir.join("meshd-wrong.log");
    let secret_wrong = rig.dir.join("wrong.secret");
    let wrong = start_meshd(
        &mut rig,
        bin,
        "node-c",
        &kubeconfig_a,
        &secret_wrong,
        &log_wrong,
    );
    let code = wait_exit(&mut rig.children[wrong], Duration::from_secs(30));
    let text = std::fs::read_to_string(&log_wrong).unwrap_or_default();
    assert_eq!(
        code, 4,
        "patching another node's Node is forbidden:\n{text}"
    );
    assert!(text.contains("node_patch_forbidden"), "{text}");
}

fn stop_namespace_processes_named(rig: &Rig, name: &str) {
    let pids = sudo(&["ip", "netns", "pids", &rig.ns]);
    for pid in String::from_utf8_lossy(&pids.stdout).split_whitespace() {
        let comm = std::fs::read_to_string(format!("/proc/{pid}/comm")).unwrap_or_default();
        if comm.trim() == name {
            let _ = sudo(&["kill", "-TERM", pid]);
        }
    }
}
