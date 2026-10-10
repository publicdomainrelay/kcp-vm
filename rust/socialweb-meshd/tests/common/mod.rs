#![allow(dead_code)]

use std::path::{Path, PathBuf};
use std::process::{Child, Command, Output};
use std::time::{Duration, Instant};

pub const READY_WAIT: Duration = Duration::from_secs(30);
pub const BIN_OVERRIDE: &str = "SOCIALWEB_MESHD_BIN";

pub fn meshd_bin() -> String {
    match std::env::var(BIN_OVERRIDE) {
        Ok(path) if !path.is_empty() => {
            let absolute = std::fs::canonicalize(&path)
                .unwrap_or_else(|err| panic!("{BIN_OVERRIDE}={path}: {err}"));
            eprintln!("meshd under test: {}", absolute.display());
            absolute.to_string_lossy().into_owned()
        }
        _ => env!("CARGO_BIN_EXE_socialweb-meshd").to_string(),
    }
}

pub fn sudo(args: &[&str]) -> Output {
    Command::new("sudo")
        .arg("-n")
        .args(args)
        .output()
        .expect("spawn sudo")
}

pub fn must(args: &[&str]) -> String {
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

pub fn gate(name: &str) -> bool {
    if std::env::var_os(name).is_none() {
        eprintln!("skipped: set {name}=1 (needs sudo -n, ip netns and CAP_NET_ADMIN)");
        return false;
    }
    assert!(
        sudo(&["true"]).status.success(),
        "{name} is set but sudo -n is not available"
    );
    true
}

pub fn scratch_dir(tag: &str) -> PathBuf {
    let base = std::env::var_os("TMPDIR")
        .map(PathBuf::from)
        .unwrap_or_else(std::env::temp_dir);
    let dir = base.join(format!("meshd-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

pub fn identity(bin: &str, secret: &Path) -> (String, String) {
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

pub fn stop_namespace_processes(ns: &str) {
    let pids = sudo(&["ip", "netns", "pids", ns]);
    for pid in String::from_utf8_lossy(&pids.stdout).split_whitespace() {
        let _ = sudo(&["kill", "-TERM", pid]);
    }
}

pub fn status(ns: &str) -> serde_json::Value {
    let script =
        "exec 3<>/dev/tcp/127.0.0.1/9469; printf 'GET /status HTTP/1.0\\r\\n\\r\\n' >&3; cat <&3";
    let raw = must(&["ip", "netns", "exec", ns, "bash", "-c", script]);
    let body = raw.split("\r\n\r\n").nth(1).expect("http body");
    serde_json::from_str(body).expect("status json")
}

pub fn wait_for_log(child: &mut Child, log: &Path, needle: &str, within: Duration) -> String {
    let deadline = Instant::now() + within;
    loop {
        let text = std::fs::read_to_string(log).unwrap_or_default();
        if text.contains(needle) {
            return text;
        }
        if let Ok(Some(status)) = child.try_wait() {
            panic!("exited {status} before {needle:?}:\n{text}");
        }
        assert!(
            Instant::now() < deadline,
            "no {needle:?} within {within:?}:\n{text}"
        );
        std::thread::sleep(Duration::from_millis(200));
    }
}

pub fn reap(child: &mut Child, within: Duration) {
    let deadline = Instant::now() + within;
    while Instant::now() < deadline {
        if let Ok(Some(_)) = child.try_wait() {
            return;
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    let _ = child.kill();
    let _ = child.wait();
}

pub fn wait_exit(child: &mut Child, within: Duration) -> i32 {
    let deadline = Instant::now() + within;
    loop {
        if let Some(status) = child.try_wait().expect("wait") {
            return status.code().unwrap_or(-1);
        }
        assert!(Instant::now() < deadline, "did not exit within {within:?}");
        std::thread::sleep(Duration::from_millis(100));
    }
}

pub fn eventually<F: FnMut() -> bool>(within: Duration, what: &str, mut check: F) {
    let deadline = Instant::now() + within;
    while !check() {
        assert!(Instant::now() < deadline, "{what} not within {within:?}");
        std::thread::sleep(Duration::from_millis(300));
    }
}
