use std::fmt;
use std::fs;
use std::io::Write;
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};

pub const ED25519_PUB_MULTICODEC: [u8; 2] = [0xed, 0x01];
pub const DID_KEY_PREFIX: &str = "did:key:z";
pub const SECRET_HEX_LEN: usize = 64;

#[derive(Debug)]
pub enum IdentityError {
    Read(PathBuf, std::io::Error),
    Write(PathBuf, std::io::Error),
    Encoding(String),
    Random(String),
}

impl fmt::Display for IdentityError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            IdentityError::Read(path, err) => write!(f, "read secret {}: {err}", path.display()),
            IdentityError::Write(path, err) => write!(f, "write secret {}: {err}", path.display()),
            IdentityError::Encoding(msg) => write!(f, "{msg}"),
            IdentityError::Random(msg) => write!(f, "generate secret: {msg}"),
        }
    }
}

impl std::error::Error for IdentityError {}

#[derive(Clone)]
pub struct Identity {
    pub secret: iroh::SecretKey,
}

impl Identity {
    pub fn from_seed(seed: &[u8; 32]) -> Identity {
        Identity {
            secret: iroh::SecretKey::from_bytes(seed),
        }
    }

    pub fn public(&self) -> [u8; 32] {
        *self.secret.public().as_bytes()
    }

    pub fn endpoint_id_hex(&self) -> String {
        endpoint_id_hex(&self.public())
    }

    pub fn did_key(&self) -> String {
        did_key(&self.public())
    }
}

pub fn endpoint_id_hex(public: &[u8; 32]) -> String {
    hex::encode(public)
}

pub fn parse_endpoint_id_hex(text: &str) -> Result<[u8; 32], IdentityError> {
    let trimmed = text.trim();
    if trimmed.len() != 64 {
        return Err(IdentityError::Encoding(format!(
            "endpoint id {trimmed:?} is {} chars, want 64 hex",
            trimmed.len()
        )));
    }
    let mut out = [0u8; 32];
    hex::decode_to_slice(trimmed, &mut out)
        .map_err(|err| IdentityError::Encoding(format!("endpoint id {trimmed:?}: {err}")))?;
    iroh::PublicKey::from_bytes(&out).map_err(|err| {
        IdentityError::Encoding(format!(
            "endpoint id {trimmed:?} is not an ed25519 point: {err}"
        ))
    })?;
    Ok(out)
}

pub fn did_key(public: &[u8; 32]) -> String {
    let mut bytes = Vec::with_capacity(34);
    bytes.extend_from_slice(&ED25519_PUB_MULTICODEC);
    bytes.extend_from_slice(public);
    format!("{DID_KEY_PREFIX}{}", bs58::encode(bytes).into_string())
}

pub fn parse_did_key(did: &str) -> Result<[u8; 32], IdentityError> {
    let encoded = did
        .strip_prefix(DID_KEY_PREFIX)
        .ok_or_else(|| IdentityError::Encoding(format!("{did:?} is not a base58btc did:key")))?;
    let bytes = bs58::decode(encoded)
        .into_vec()
        .map_err(|err| IdentityError::Encoding(format!("{did:?}: {err}")))?;
    if bytes.len() != 34 || bytes[0..2] != ED25519_PUB_MULTICODEC {
        return Err(IdentityError::Encoding(format!(
            "{did:?} does not carry an ed25519-pub multicodec key"
        )));
    }
    let mut out = [0u8; 32];
    out.copy_from_slice(&bytes[2..]);
    Ok(out)
}

pub fn parse_secret_hex(text: &str) -> Result<[u8; 32], IdentityError> {
    let trimmed = text.trim();
    if trimmed.len() != SECRET_HEX_LEN {
        return Err(IdentityError::Encoding(format!(
            "secret is {} chars, want {SECRET_HEX_LEN} hex (the IROH_SECRET encoding)",
            trimmed.len()
        )));
    }
    let mut out = [0u8; 32];
    hex::decode_to_slice(trimmed, &mut out)
        .map_err(|err| IdentityError::Encoding(format!("secret is not hex: {err}")))?;
    Ok(out)
}

pub enum Loaded {
    Existing(Identity),
    Created(Identity),
}

impl Loaded {
    pub fn identity(&self) -> &Identity {
        match self {
            Loaded::Existing(identity) | Loaded::Created(identity) => identity,
        }
    }
}

pub fn load_or_create(path: &Path) -> Result<Loaded, IdentityError> {
    match fs::read_to_string(path) {
        Ok(text) => {
            let seed = parse_secret_hex(&text).map_err(|err| {
                IdentityError::Encoding(format!("secret file {}: {err}", path.display()))
            })?;
            Ok(Loaded::Existing(Identity::from_seed(&seed)))
        }
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => {
            let mut seed = [0u8; 32];
            getrandom::fill(&mut seed).map_err(|err| IdentityError::Random(err.to_string()))?;
            write_secret(path, &seed)?;
            Ok(Loaded::Created(Identity::from_seed(&seed)))
        }
        Err(err) => Err(IdentityError::Read(path.to_path_buf(), err)),
    }
}

fn write_secret(path: &Path, seed: &[u8; 32]) -> Result<(), IdentityError> {
    let write_err = |err| IdentityError::Write(path.to_path_buf(), err);
    let parent = match path.parent() {
        Some(parent) if !parent.as_os_str().is_empty() => parent.to_path_buf(),
        _ => PathBuf::from("."),
    };
    fs::create_dir_all(&parent).map_err(write_err)?;
    let file_name = path
        .file_name()
        .map(|name| name.to_string_lossy().into_owned())
        .unwrap_or_else(|| "secret".to_string());
    let staging = parent.join(format!(".{file_name}.{}.tmp", std::process::id()));
    let mut file = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(&staging)
        .map_err(write_err)?;
    let body = format!("{}\n", hex::encode(seed));
    let written = file
        .write_all(body.as_bytes())
        .and_then(|()| file.sync_all());
    if let Err(err) = written {
        let _ = fs::remove_file(&staging);
        return Err(write_err(err));
    }
    fs::rename(&staging, path).map_err(|err| {
        let _ = fs::remove_file(&staging);
        write_err(err)
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    const SPEC_PUBLIC_KEY_HEX: &str =
        "3b6a27bcceb6a42d62a3a8d02a6f0d73653215771de243a63ac048a18b59da29";
    const SPEC_DID: &str = "did:key:z6MkiTBz1ymuepAQ4HEHYSF1H8quG5GLVVQR3djdX3mDooWp";
    const DUMBPIPE_SECRET_HEX: &str =
        "f8555129991792f402f21f6a01b0c027744b0b02c4b5d1f0e1bf979f722991d8";
    const DUMBPIPE_ENDPOINT_ID: &str =
        "cb6686656591d6e18858e0262a30c92700543f3230d3d483a4976cbfcbfc693c";

    fn spec_public() -> [u8; 32] {
        let mut out = [0u8; 32];
        hex::decode_to_slice(SPEC_PUBLIC_KEY_HEX, &mut out).unwrap();
        out
    }

    #[test]
    fn the_did_key_spec_vector_encodes_an_ed25519_public_key() {
        assert_eq!(did_key(&spec_public()), SPEC_DID);
    }

    #[test]
    fn the_did_key_spec_vector_decodes_back_to_its_public_key() {
        assert_eq!(parse_did_key(SPEC_DID).unwrap(), spec_public());
    }

    #[test]
    fn the_endpoint_id_of_a_dumbpipe_iroh_secret_is_the_seeds_public_key_in_hex() {
        let seed = parse_secret_hex(DUMBPIPE_SECRET_HEX).unwrap();
        let identity = Identity::from_seed(&seed);
        assert_eq!(identity.endpoint_id_hex(), DUMBPIPE_ENDPOINT_ID);
        assert_eq!(identity.secret.public().to_string(), DUMBPIPE_ENDPOINT_ID);
    }

    #[test]
    fn the_did_and_the_endpoint_id_name_one_key() {
        let seed = parse_secret_hex(DUMBPIPE_SECRET_HEX).unwrap();
        let identity = Identity::from_seed(&seed);
        let from_did = parse_did_key(&identity.did_key()).unwrap();
        assert_eq!(endpoint_id_hex(&from_did), identity.endpoint_id_hex());
        assert_eq!(
            parse_endpoint_id_hex(DUMBPIPE_ENDPOINT_ID).unwrap(),
            from_did
        );
    }

    #[test]
    fn a_did_key_of_another_key_type_is_refused() {
        let secp256k1_did = "did:key:zQ3shokFTS3brHcDQrn82RUDfCZESWL1ZdCEJwekUDPQiYBme";
        assert!(parse_did_key(secp256k1_did).is_err());
        assert!(parse_did_key("did:web:example.com").is_err());
    }

    #[test]
    fn a_secret_must_be_sixty_four_hex_chars_and_surrounding_whitespace_is_ignored() {
        assert!(parse_secret_hex(&format!("  {DUMBPIPE_SECRET_HEX}\n")).is_ok());
        assert!(parse_secret_hex(&DUMBPIPE_SECRET_HEX[..62]).is_err());
        assert!(parse_secret_hex(&format!("{}zz", &DUMBPIPE_SECRET_HEX[..62])).is_err());
    }

    #[test]
    fn an_absent_secret_file_is_created_0600_and_read_back_as_the_same_identity() {
        use std::os::unix::fs::PermissionsExt;
        let dir = std::env::temp_dir().join(format!(
            "meshd-identity-test-{}-{}",
            std::process::id(),
            line!()
        ));
        let _ = fs::remove_dir_all(&dir);
        let path = dir.join("nested").join("secret");
        let created = match load_or_create(&path).unwrap() {
            Loaded::Created(identity) => identity,
            Loaded::Existing(_) => panic!("a missing file was reported as existing"),
        };
        let mode = fs::metadata(&path).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode, 0o600);
        let text = fs::read_to_string(&path).unwrap();
        assert_eq!(text.trim().len(), SECRET_HEX_LEN);
        let again = match load_or_create(&path).unwrap() {
            Loaded::Existing(identity) => identity,
            Loaded::Created(_) => panic!("an existing file was overwritten"),
        };
        assert_eq!(again.endpoint_id_hex(), created.endpoint_id_hex());
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_corrupt_secret_file_is_an_error_and_is_left_alone() {
        let dir = std::env::temp_dir().join(format!(
            "meshd-identity-test-{}-{}",
            std::process::id(),
            line!()
        ));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        let path = dir.join("secret");
        fs::write(&path, "not a key\n").unwrap();
        assert!(load_or_create(&path).is_err());
        assert_eq!(fs::read_to_string(&path).unwrap(), "not a key\n");
        let _ = fs::remove_dir_all(&dir);
    }
}
