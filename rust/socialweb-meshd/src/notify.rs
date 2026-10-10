use std::os::linux::net::SocketAddrExt;
use std::os::unix::net::{SocketAddr, UnixDatagram};

pub const NOTIFY_SOCKET_ENV: &str = "NOTIFY_SOCKET";

pub fn notify_target(value: &str) -> std::io::Result<SocketAddr> {
    match value.strip_prefix('@') {
        Some(abstract_name) => SocketAddr::from_abstract_name(abstract_name.as_bytes()),
        None => SocketAddr::from_pathname(value),
    }
}

pub fn sd_notify(state: &str) -> std::io::Result<bool> {
    let Some(target) = std::env::var_os(NOTIFY_SOCKET_ENV) else {
        return Ok(false);
    };
    let target = notify_target(&target.to_string_lossy())?;
    let socket = UnixDatagram::unbound()?;
    socket.send_to_addr(state.as_bytes(), &target)?;
    Ok(true)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_notify_socket_path_receives_the_state_line() {
        let dir = std::env::temp_dir().join(format!("meshd-notify-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("notify.sock");
        let receiver = UnixDatagram::bind(&path).unwrap();
        let target = notify_target(path.to_str().unwrap()).unwrap();
        let socket = UnixDatagram::unbound().unwrap();
        socket.send_to_addr(b"READY=1", &target).unwrap();
        let mut buf = [0u8; 64];
        let n = receiver.recv(&mut buf).unwrap();
        assert_eq!(&buf[..n], b"READY=1");
        let _ = std::fs::remove_dir_all(&dir);
    }
}
