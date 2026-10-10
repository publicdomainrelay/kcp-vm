use std::fs::File;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Output, Stdio};
use std::time::{Duration, Instant};

const GATE: &str = "SOCIALWEB_MESHD_NETNS";
const READY_WAIT: Duration = Duration::from_secs(30);
const UNDERLAY_MTU: &str = "1228";

fn sudo(args: &[&str]) -> Output {
    Command::new("sudo")
        .arg("-n")
        .args(args)
        .output()
        .expect("spawn sudo")
}

fn must(args: &[&str]) -> String {
    let out = sudo(args);
    assert!(
        out.status.success(),
        "sudo {} failed: {}{}",
        args.join(" "),
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    String::from_utf8_lossy(&out.stdout).into_owned()
}

struct Side {
    node_ns: String,
    pod_ns: String,
    node_name: &'static str,
    underlay: &'static str,
    pod_cidr: &'static str,
    pod_gateway: &'static str,
    pod_ip: &'static str,
    secret: PathBuf,
    log: PathBuf,
    endpoint: String,
    did: String,
    child: Option<Child>,
}

struct Rig {
    dir: PathBuf,
    peers: PathBuf,
    a: Side,
    b: Side,
}

impl Rig {
    fn namespaces(&self) -> Vec<String> {
        vec![
            self.a.node_ns.clone(),
            self.b.node_ns.clone(),
            self.a.pod_ns.clone(),
            self.b.pod_ns.clone(),
        ]
    }
}

fn stop_namespace_processes(ns: &str) {
    let pids = sudo(&["ip", "netns", "pids", ns]);
    for pid in String::from_utf8_lossy(&pids.stdout).split_whitespace() {
        let _ = sudo(&["kill", "-TERM", pid]);
    }
}

impl Drop for Rig {
    fn drop(&mut self) {
        for ns in self.namespaces() {
            stop_namespace_processes(&ns);
        }
        for side in [&mut self.a, &mut self.b] {
            if let Some(mut child) = side.child.take() {
                let deadline = Instant::now() + Duration::from_secs(5);
                while Instant::now() < deadline {
                    if let Ok(Some(_)) = child.try_wait() {
                        break;
                    }
                    std::thread::sleep(Duration::from_millis(100));
                }
                let _ = child.kill();
                let _ = child.wait();
            }
        }
        for ns in self.namespaces() {
            let _ = sudo(&["ip", "netns", "del", &ns]);
        }
        let _ = std::fs::remove_dir_all(&self.dir);
    }
}

fn identity(bin: &str, secret: &Path) -> (String, String) {
    let out = Command::new(bin)
        .args(["identity", "--secret-file"])
        .arg(secret)
        .output()
        .expect("run identity");
    assert!(
        out.status.success(),
        "identity: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    let value: serde_json::Value = serde_json::from_slice(&out.stdout).expect("identity json");
    (
        value["endpoint"].as_str().unwrap().to_string(),
        value["did"].as_str().unwrap().to_string(),
    )
}

fn plumb(side: &Side, peer_underlay_ns: Option<&str>) {
    for ns in [&side.node_ns, &side.pod_ns] {
        must(&["ip", "-n", ns, "link", "set", "lo", "up"]);
    }
    if let Some(peer_ns) = peer_underlay_ns {
        must(&[
            "ip",
            "-n",
            &side.node_ns,
            "link",
            "add",
            "u0",
            "type",
            "veth",
            "peer",
            "name",
            "u0",
            "netns",
            peer_ns,
        ]);
    }
    must(&[
        "ip",
        "-n",
        &side.node_ns,
        "addr",
        "add",
        &format!("{}/24", side.underlay),
        "dev",
        "u0",
    ]);
    must(&[
        "ip",
        "-n",
        &side.node_ns,
        "link",
        "set",
        "u0",
        "mtu",
        UNDERLAY_MTU,
        "up",
    ]);
    must(&[
        "ip",
        "-n",
        &side.pod_ns,
        "link",
        "add",
        "eth0",
        "type",
        "veth",
        "peer",
        "name",
        "pod0",
        "netns",
        &side.node_ns,
    ]);
    must(&[
        "ip",
        "-n",
        &side.pod_ns,
        "addr",
        "add",
        &format!("{}/24", side.pod_ip),
        "dev",
        "eth0",
    ]);
    must(&["ip", "-n", &side.pod_ns, "link", "set", "eth0", "up"]);
    must(&[
        "ip",
        "-n",
        &side.pod_ns,
        "route",
        "add",
        "default",
        "via",
        side.pod_gateway,
    ]);
    must(&[
        "ip",
        "-n",
        &side.node_ns,
        "addr",
        "add",
        &format!("{}/24", side.pod_gateway),
        "dev",
        "pod0",
    ]);
    must(&["ip", "-n", &side.node_ns, "link", "set", "pod0", "up"]);
    must(&[
        "ip",
        "netns",
        "exec",
        &side.node_ns,
        "sysctl",
        "-qw",
        "net.ipv4.ip_forward=1",
    ]);
}

fn node_json(side: &Side) -> serde_json::Value {
    serde_json::json!({
        "metadata": {
            "name": side.node_name,
            "annotations": {
                "socialweb.computer/mesh-endpoint": side.endpoint,
                "socialweb.computer/mesh-did": side.did,
                "socialweb.computer/mesh-addrs": format!("{}:47001", side.underlay),
            }
        },
        "spec": {"podCIDR": side.pod_cidr, "podCIDRs": [side.pod_cidr]}
    })
}

fn start(bin: &str, side: &mut Side, peers: &Path) {
    let log = File::create(&side.log).expect("log file");
    let child = Command::new("sudo")
        .args(["-n", "ip", "netns", "exec", &side.node_ns, bin, "run"])
        .args(["--node-name", side.node_name])
        .arg("--peers-file")
        .arg(peers)
        .arg("--secret-file")
        .arg(&side.secret)
        .args(["--relay", "disabled", "--discovery", "none"])
        .args(["--bind", &format!("{}:47001", side.underlay)])
        .args(["--metrics-listen", "127.0.0.1:9469"])
        .args(["--log-filter", "info,socialweb_meshd=debug"])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::from(log))
        .spawn()
        .expect("spawn meshd");
    side.child = Some(child);
}

fn wait_ready(side: &mut Side) {
    let deadline = Instant::now() + READY_WAIT;
    loop {
        let log = std::fs::read_to_string(&side.log).unwrap_or_default();
        if log.contains("\"event\":\"ready\"") {
            return;
        }
        if let Some(status) = side
            .child
            .as_mut()
            .and_then(|c| c.try_wait().ok().flatten())
        {
            panic!("{} exited {status} before ready:\n{log}", side.node_name);
        }
        assert!(
            Instant::now() < deadline,
            "{} not ready within {READY_WAIT:?}:\n{log}",
            side.node_name
        );
        std::thread::sleep(Duration::from_millis(200));
    }
}

fn ping(ns: &str, target: &str, extra: &[&str]) -> Output {
    let mut args = vec![
        "ip", "netns", "exec", ns, "ping", "-n", "-c", "3", "-i", "0.2", "-W", "3",
    ];
    args.extend_from_slice(extra);
    args.push(target);
    sudo(&args)
}

fn assert_ping(ns: &str, target: &str, extra: &[&str]) {
    let out = ping(ns, target, extra);
    assert!(
        out.status.success(),
        "ping {target} from {ns} {extra:?} failed:\n{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
}

fn status(ns: &str) -> serde_json::Value {
    let script =
        "exec 3<>/dev/tcp/127.0.0.1/9469; printf 'GET /status HTTP/1.0\\r\\n\\r\\n' >&3; cat <&3";
    let raw = must(&["ip", "netns", "exec", ns, "bash", "-c", script]);
    let body = raw.split("\r\n\r\n").nth(1).expect("http body");
    serde_json::from_str(body).expect("status json")
}

fn stop(side: &mut Side) -> i32 {
    stop_namespace_processes(&side.node_ns);
    let mut child = side.child.take().expect("running");
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        if let Some(status) = child.try_wait().expect("wait") {
            return status.code().unwrap_or(-1);
        }
        assert!(
            Instant::now() < deadline,
            "{} did not stop on SIGTERM",
            side.node_name
        );
        std::thread::sleep(Duration::from_millis(100));
    }
}

fn side(dir: &Path, tag: &str, node_name: &'static str, underlay: &'static str, n: u8) -> Side {
    let pid = std::process::id();
    let (pod_cidr, pod_gateway, pod_ip): (&'static str, &'static str, &'static str) = match n {
        1 => ("10.42.1.0/24", "10.42.1.1", "10.42.1.2"),
        _ => ("10.42.2.0/24", "10.42.2.1", "10.42.2.2"),
    };
    Side {
        node_ns: format!("swmesh-{pid}-node-{tag}"),
        pod_ns: format!("swmesh-{pid}-pod-{tag}"),
        node_name,
        underlay,
        pod_cidr,
        pod_gateway,
        pod_ip,
        secret: dir.join(format!("{tag}.secret")),
        log: dir.join(format!("{tag}.log")),
        endpoint: String::new(),
        did: String::new(),
        child: None,
    }
}

#[test]
fn two_meshd_in_two_netns_carry_ping_between_two_pod_cidrs() {
    if std::env::var_os(GATE).is_none() {
        eprintln!("skipped: set {GATE}=1 (needs sudo -n, ip netns and CAP_NET_ADMIN)");
        return;
    }
    assert!(
        sudo(&["true"]).status.success(),
        "{GATE} is set but sudo -n is not available"
    );
    let bin = env!("CARGO_BIN_EXE_socialweb-meshd");
    let base = std::env::var_os("TMPDIR")
        .map(PathBuf::from)
        .unwrap_or_else(std::env::temp_dir);
    let dir = base.join(format!("meshd-netns-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();

    let mut rig = Rig {
        peers: dir.join("peers.json"),
        a: side(&dir, "a", "node-a", "10.199.0.1", 1),
        b: side(&dir, "b", "node-b", "10.199.0.2", 2),
        dir,
    };
    for ns in rig.namespaces() {
        must(&["ip", "netns", "add", &ns]);
    }
    let b_ns = rig.b.node_ns.clone();
    plumb(&rig.a, Some(&b_ns));
    plumb(&rig.b, None);

    (rig.a.endpoint, rig.a.did) = identity(bin, &rig.a.secret);
    (rig.b.endpoint, rig.b.did) = identity(bin, &rig.b.secret);
    let peers = serde_json::json!({
        "apiVersion": "v1",
        "kind": "NodeList",
        "items": [node_json(&rig.a), node_json(&rig.b)],
    });
    std::fs::write(&rig.peers, peers.to_string()).unwrap();

    let peers_path = rig.peers.clone();
    start(bin, &mut rig.a, &peers_path);
    start(bin, &mut rig.b, &peers_path);
    wait_ready(&mut rig.a);
    wait_ready(&mut rig.b);

    let routes = must(&[
        "ip",
        "-n",
        &rig.a.node_ns,
        "route",
        "show",
        "dev",
        "swmesh0",
    ]);
    assert!(
        routes.contains("10.42.2.0/24"),
        "node a routes via swmesh0: {routes}"
    );
    assert!(
        routes.contains("src 10.42.1.0"),
        "node a route source: {routes}"
    );

    assert_ping(&rig.a.pod_ns, rig.b.pod_ip, &[]);
    assert_ping(&rig.b.pod_ns, rig.a.pod_ip, &[]);
    assert_ping(&rig.a.node_ns, rig.b.pod_ip, &[]);

    let a_status = status(&rig.a.node_ns);
    eprintln!("node a status: {a_status}");
    let mtu = a_status["mtu"].as_u64().unwrap();
    let largest_icmp_payload = (mtu - 28).to_string();
    assert_ping(
        &rig.a.pod_ns,
        rig.b.pod_ip,
        &["-M", "do", "-s", &largest_icmp_payload],
    );
    let max_datagram = a_status["routes"][0]["maxDatagramSize"].as_u64();
    eprintln!("tun mtu {mtu}; max datagram size reported on the live connection: {max_datagram:?}");
    assert!(
        max_datagram.is_some_and(|size| size >= mtu),
        "the tun MTU {mtu} must fit the connection's datagram size {max_datagram:?}"
    );
    assert_ping(&rig.a.pod_ns, rig.b.pod_ip, &["-M", "dont", "-s", "1400"]);
    let oversized = ping(
        &rig.a.pod_ns,
        rig.b.pod_ip,
        &["-c", "1", "-M", "do", "-s", "1400"],
    );
    assert!(
        !oversized.status.success(),
        "a don't-fragment packet above the tun MTU must be refused by the node, not sent"
    );

    let unknown = ping(&rig.a.pod_ns, "10.42.9.9", &["-c", "1"]);
    assert!(
        !unknown.status.success(),
        "a destination outside every pod CIDR must not answer"
    );

    let a_status = status(&rig.a.node_ns);
    assert_eq!(a_status["ready"], true);
    assert_eq!(a_status["routes"][0]["node"], "node-b");
    assert_eq!(a_status["routes"][0]["connected"], true);
    assert!(a_status["counters"]["datagrams_sent"].as_u64().unwrap() >= 9);
    assert!(a_status["counters"]["datagrams_received"].as_u64().unwrap() >= 9);
    assert_eq!(a_status["counters"]["dropped_too_large"], 0);

    assert_eq!(stop(&mut rig.b), 0, "SIGTERM is a clean exit");
    let gone = sudo(&["ip", "-n", &rig.b.node_ns, "link", "show", "swmesh0"]);
    assert!(
        !gone.status.success(),
        "the tun device leaves with the process"
    );
    start(bin, &mut rig.b, &peers_path);
    wait_ready(&mut rig.b);
    assert_ping(&rig.a.pod_ns, rig.b.pod_ip, &["-c", "5"]);
    assert_ping(&rig.b.pod_ns, rig.a.pod_ip, &[]);
}
