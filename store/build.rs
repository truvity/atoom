//! Compiles `proto/**/*.proto` into buffa messages under `OUT_DIR`. The generated code is never committed for Rust (the Go code is, in
//! `gen/`): the build makes it, as it does in sluis. The runtime is buffa, the one `connectrpc` 0.9 emits.

use std::path::{Path, PathBuf};

fn collect(dir: &Path, out: &mut Vec<PathBuf>) -> std::io::Result<()> {
    let mut entries: Vec<_> = std::fs::read_dir(dir)?.collect::<Result<_, _>>()?;
    entries.sort_by_key(std::fs::DirEntry::path);
    for e in entries {
        let p = e.path();
        if p.is_dir() {
            collect(&p, out)?;
        } else if p.extension().is_some_and(|x| x == "proto") {
            out.push(p);
        }
    }
    Ok(())
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let manifest = PathBuf::from(std::env::var("CARGO_MANIFEST_DIR")?);
    let root = manifest.join("proto");
    // The proto this contract imports: the common library's. `connectrpc-build` cannot point a message at another crate (its unified
    // path ignores `extern_path`), so this crate generates its own wire-identical copy of those messages, as the crates of consumers
    // do (common/PROVENANCE.md, step 1). The copy lives in the package `atoom.common.v1alpha1` of this crate.
    let common = manifest.join("../common/proto");
    // Cargo scans a directory recursively by mtime, so a changed .proto anywhere under it reruns this script.
    println!("cargo:rerun-if-changed={}", root.display());
    println!("cargo:rerun-if-changed={}", common.display());
    let mut files = Vec::new();
    collect(&root, &mut files)?;
    collect(&common, &mut files)?;
    if files.is_empty() {
        return Err(format!("no .proto file under {}", root.display()).into());
    }
    connectrpc_build::Config::new()
        .files(&files)
        .includes(&[&root, &common])
        .include_file("_connectrpc.rs")
        .compile()?;
    Ok(())
}
