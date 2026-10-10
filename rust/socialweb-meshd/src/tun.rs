use std::ffi::CString;
use std::io;
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};

use tokio::io::Interest;
use tokio::io::unix::AsyncFd;

pub const TUN_CLONE_DEVICE: &str = "/dev/net/tun";

pub struct Tun {
    fd: AsyncFd<OwnedFd>,
    name: String,
}

impl Tun {
    pub fn create(name: &str) -> io::Result<Tun> {
        if name.is_empty() || name.len() >= libc::IFNAMSIZ {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                format!("tun name {name:?} must be 1..{} bytes", libc::IFNAMSIZ - 1),
            ));
        }
        let path = CString::new(TUN_CLONE_DEVICE).map_err(io::Error::other)?;
        let raw = unsafe {
            libc::open(
                path.as_ptr(),
                libc::O_RDWR | libc::O_NONBLOCK | libc::O_CLOEXEC,
            )
        };
        if raw < 0 {
            return Err(io::Error::last_os_error());
        }
        let owned = unsafe { OwnedFd::from_raw_fd(raw) };
        let mut request: libc::ifreq = unsafe { std::mem::zeroed() };
        for (slot, byte) in request.ifr_name.iter_mut().zip(name.as_bytes()) {
            *slot = *byte as libc::c_char;
        }
        request.ifr_ifru.ifru_flags = (libc::IFF_TUN | libc::IFF_NO_PI) as libc::c_short;
        let rc = unsafe { libc::ioctl(owned.as_raw_fd(), libc::TUNSETIFF, &mut request) };
        if rc < 0 {
            return Err(io::Error::last_os_error());
        }
        let assigned: Vec<u8> = request
            .ifr_name
            .iter()
            .take_while(|c| **c != 0)
            .map(|c| *c as u8)
            .collect();
        Ok(Tun {
            fd: AsyncFd::new(owned)?,
            name: String::from_utf8_lossy(&assigned).into_owned(),
        })
    }

    pub fn name(&self) -> &str {
        &self.name
    }

    pub fn index(&self) -> io::Result<u32> {
        let name = CString::new(self.name.as_str()).map_err(io::Error::other)?;
        let index = unsafe { libc::if_nametoindex(name.as_ptr()) };
        if index == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(index)
    }

    pub async fn recv(&self, buf: &mut [u8]) -> io::Result<usize> {
        self.fd
            .async_io(Interest::READABLE, |fd| {
                let n = unsafe { libc::read(fd.as_raw_fd(), buf.as_mut_ptr().cast(), buf.len()) };
                if n < 0 {
                    Err(io::Error::last_os_error())
                } else {
                    Ok(n as usize)
                }
            })
            .await
    }

    pub async fn send(&self, packet: &[u8]) -> io::Result<usize> {
        self.fd
            .async_io(Interest::WRITABLE, |fd| {
                let n =
                    unsafe { libc::write(fd.as_raw_fd(), packet.as_ptr().cast(), packet.len()) };
                if n < 0 {
                    Err(io::Error::last_os_error())
                } else {
                    Ok(n as usize)
                }
            })
            .await
    }
}
