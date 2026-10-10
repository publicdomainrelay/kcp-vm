use std::path::{Path, PathBuf};

fn rust_files(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            if path
                .file_name()
                .is_some_and(|n| n == "target" || n == "dist")
            {
                continue;
            }
            rust_files(&path, out);
        } else if path.extension().is_some_and(|e| e == "rs") {
            out.push(path);
        }
    }
}

fn is_ident(c: u8) -> bool {
    c.is_ascii_alphanumeric() || c == b'_'
}

pub fn comment_lines(source: &str) -> Vec<usize> {
    let bytes = source.as_bytes();
    let mut found = Vec::new();
    let mut line = 1;
    let mut i = 0;
    while i < bytes.len() {
        let c = bytes[i];
        if c == b'\n' {
            line += 1;
            i += 1;
            continue;
        }
        if c == b'/' && bytes.get(i + 1) == Some(&b'/') {
            found.push(line);
            while i < bytes.len() && bytes[i] != b'\n' {
                i += 1;
            }
            continue;
        }
        if c == b'/' && bytes.get(i + 1) == Some(&b'*') {
            found.push(line);
            let mut depth = 1;
            i += 2;
            while i < bytes.len() && depth > 0 {
                if bytes[i] == b'\n' {
                    line += 1;
                } else if bytes[i] == b'/' && bytes.get(i + 1) == Some(&b'*') {
                    depth += 1;
                    i += 1;
                } else if bytes[i] == b'*' && bytes.get(i + 1) == Some(&b'/') {
                    depth -= 1;
                    i += 1;
                }
                i += 1;
            }
            continue;
        }
        let prev_ident = i > 0 && is_ident(bytes[i - 1]);
        if (c == b'r' || (c == b'b' && bytes.get(i + 1) == Some(&b'r'))) && !prev_ident {
            let mut j = if c == b'b' { i + 2 } else { i + 1 };
            let mut hashes = 0;
            while bytes.get(j) == Some(&b'#') {
                hashes += 1;
                j += 1;
            }
            if bytes.get(j) == Some(&b'"') {
                j += 1;
                loop {
                    match bytes.get(j) {
                        None => break,
                        Some(b'\n') => line += 1,
                        Some(b'"')
                            if bytes[j + 1..]
                                .iter()
                                .take(hashes)
                                .filter(|b| **b == b'#')
                                .count()
                                == hashes =>
                        {
                            j += 1 + hashes;
                            break;
                        }
                        _ => {}
                    }
                    j += 1;
                }
                i = j;
                continue;
            }
        }
        if c == b'"' {
            i += 1;
            while i < bytes.len() && bytes[i] != b'"' {
                if bytes[i] == b'\\' {
                    i += 1;
                }
                if bytes.get(i) == Some(&b'\n') {
                    line += 1;
                }
                i += 1;
            }
            i += 1;
            continue;
        }
        if c == b'\'' {
            if bytes.get(i + 1) == Some(&b'\\') {
                i += 2;
                while i < bytes.len() && bytes[i] != b'\'' {
                    i += 1;
                }
                i += 1;
                continue;
            }
            if bytes.get(i + 2) == Some(&b'\'') {
                i += 3;
                continue;
            }
        }
        i += 1;
    }
    found
}

#[test]
fn the_comment_scanner_sees_comments_and_not_strings() {
    let source = "let a = \"// not\";\nlet b = r#\"/* not */\"#;\nlet c = '/';\nfn f<'a>(x: &'a str) {}\n// yes\nlet d = 1; /* yes */\n/// yes\n";
    assert_eq!(comment_lines(source), vec![5, 6, 7]);
}

#[test]
fn no_rust_source_has_a_comment_or_a_non_ascii_byte() {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    let workspace = root.parent().map(Path::to_path_buf).unwrap_or(root);
    let mut files = Vec::new();
    rust_files(&workspace, &mut files);
    assert!(
        !files.is_empty(),
        "no .rs files under {}",
        workspace.display()
    );
    let mut problems = Vec::new();
    for file in files {
        let text = std::fs::read_to_string(&file).unwrap();
        if let Some(pos) = text.bytes().position(|b| !b.is_ascii()) {
            problems.push(format!(
                "{}: non-ASCII byte at offset {pos}",
                file.display()
            ));
        }
        for line in comment_lines(&text) {
            problems.push(format!("{}:{line}: comment", file.display()));
        }
    }
    assert!(
        problems.is_empty(),
        "names and types carry meaning; decisions live in docs/ARCHITECTURE.md:\n{}",
        problems.join("\n")
    );
}
