//! The guard of the thin main `index-sqlite`, through its binary: the flags `--allow-uid`, `--socket-gid` and `--socket-mode` reach the socket,
//! a caller whose uid is not admitted is refused with the reason `caller` before its message is read, and `version` answers. The
//! guard itself is tested in `atoom-socket`; this file proves that this main wires it.

#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::io::{Read, Write};
use std::os::unix::fs::{MetadataExt, PermissionsExt};
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

const BIN: &str = env!("CARGO_BIN_EXE_index-sqlite");
const PROCEDURE: &str = "/atoom.index.v1alpha2.IndexService/GetCursor";
const READ_PROCEDURE: &str = "/atoom.index.v1alpha2.IndexReadService/Search";

/// A main that serves, and the directory that holds its socket and its state.
struct Running {
    child: Child,
    dir: tempfile::TempDir,
}

impl Drop for Running {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

fn owner(path: &Path) -> (u32, u32) {
    let m = std::fs::metadata(path).unwrap();
    (m.uid(), m.gid())
}

/// The first socket of the main.
fn socket(dir: &Path) -> PathBuf {
    dir.join("s.sock")
}

/// The backend suite's declaration, written into `dir`.
fn declaration(dir: &std::path::Path) -> std::path::PathBuf {
    let f = dir.join("declaration.json");
    std::fs::write(&f, atoom_index_sqlite::suite::DECLARATION).unwrap();
    f
}

fn start(extra: &[&str]) -> Running {
    let dir = tempfile::tempdir().unwrap();
    let mut cmd = Command::new(BIN);
    cmd.arg("serve");
    let state = dir.path().join("state");
    std::fs::create_dir(&state).unwrap();
    std::fs::set_permissions(&state, std::fs::Permissions::from_mode(0o700)).unwrap();
    cmd.arg("--socket")
        .arg(socket(dir.path()))
        .arg("--read-socket")
        .arg(dir.path().join("read.sock"))
        .arg("--state-dir")
        .arg(&state)
        .arg("--declaration")
        .arg(declaration(dir.path()));
    cmd.args(extra)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    let mut child = cmd.spawn().expect("run the binary");
    let until = Instant::now() + Duration::from_secs(20);
    for s in ["s.sock", "read.sock"] {
        loop {
            if UnixStream::connect(dir.path().join(s)).is_ok() {
                break;
            }
            if let Some(status) = child.try_wait().unwrap() {
                panic!("the main ended before it served: {status}");
            }
            assert!(Instant::now() < until, "no socket in 20 seconds");
            std::thread::sleep(Duration::from_millis(20));
        }
    }
    Running { child, dir }
}

/// One Connect unary call with a JSON body of `{}` over HTTP/1.1: the status and the whole answer.
fn call(socket: &Path, procedure: &str) -> (u16, String) {
    let mut s = UnixStream::connect(socket).unwrap();
    s.set_read_timeout(Some(Duration::from_secs(20))).unwrap();
    let body = b"{}";
    write!(
        s,
        "POST {procedure} HTTP/1.1\r\nHost: unix\r\nContent-Type: application/json\r\nConnect-Protocol-Version: 1\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
        body.len()
    )
    .unwrap();
    s.write_all(body).unwrap();
    let mut out = String::new();
    s.read_to_string(&mut out).unwrap();
    let status = out
        .split_whitespace()
        .nth(1)
        .and_then(|c| c.parse().ok())
        .unwrap_or(0);
    (status, out)
}

fn me() -> u32 {
    owner(tempfile::tempdir().unwrap().path()).0
}

fn assert_refused_as_caller(status: u16, answer: &str) {
    assert_eq!(status, 403, "{answer}");
    assert!(answer.contains("permission_denied"), "{answer}");
    assert!(answer.contains("caller"), "{answer}");
}

fn assert_admitted(status: u16, answer: &str) {
    assert!(
        status != 403 && !answer.contains("permission_denied"),
        "the guard refused a caller it should admit: {answer}"
    );
}

#[test]
fn the_default_admits_the_uid_of_the_process() {
    let r = start(&[]);
    let (status, answer) = call(&socket(r.dir.path()), PROCEDURE);
    assert_admitted(status, &answer);
}

#[test]
fn a_uid_outside_allow_uid_is_refused_with_the_reason_caller() {
    let other = (me() + 1).to_string();
    let r = start(&["--allow-uid", &other]);
    let (status, answer) = call(&socket(r.dir.path()), PROCEDURE);
    assert_refused_as_caller(status, &answer);
}

#[test]
fn a_listed_uid_is_admitted() {
    let (mine, other) = (me().to_string(), (me() + 1).to_string());
    let r = start(&["--allow-uid", &other, "--allow-uid", &mine]);
    let (status, answer) = call(&socket(r.dir.path()), PROCEDURE);
    assert_admitted(status, &answer);
}

#[test]
fn any_uid_turns_the_check_off() {
    let r = start(&["--allow-any-uid"]);
    let (status, answer) = call(&socket(r.dir.path()), PROCEDURE);
    assert_admitted(status, &answer);
}

#[test]
fn the_socket_group_and_mode_are_applied() {
    let gid = owner(tempfile::tempdir().unwrap().path()).1.to_string();
    // A group alone opens the mode to 0660 for it; a mode of its own wins.
    let r = start(&["--socket-gid", &gid]);
    let path = socket(r.dir.path());
    assert_eq!(
        std::fs::metadata(&path).unwrap().permissions().mode() & 0o777,
        0o660
    );
    assert_eq!(owner(&path).1.to_string(), gid);
    drop(r);
    let r = start(&["--socket-gid", &gid, "--socket-mode", "0640"]);
    assert_eq!(
        std::fs::metadata(socket(r.dir.path()))
            .unwrap()
            .permissions()
            .mode()
            & 0o777,
        0o640
    );
    drop(r);
    let r = start(&[]);
    assert_eq!(
        std::fs::metadata(socket(r.dir.path()))
            .unwrap()
            .permissions()
            .mode()
            & 0o777,
        0o600,
        "the default mode"
    );
}

#[test]
fn the_read_socket_has_its_own_callers_and_defaults_to_the_same() {
    let (mine, other) = (me().to_string(), (me() + 1).to_string());
    // The write side admits only another uid; the read side is told it admits this one.
    let r = start(&["--allow-uid", &other, "--read-allow-uid", &mine]);
    let (status, answer) = call(&socket(r.dir.path()), PROCEDURE);
    assert_refused_as_caller(status, &answer);
    let (status, answer) = call(&r.dir.path().join("read.sock"), READ_PROCEDURE);
    assert_admitted(status, &answer);
    drop(r);
    // Without `--read-allow-uid` the read side admits the callers of `--allow-uid`.
    let r = start(&["--allow-uid", &other]);
    let (status, answer) = call(&r.dir.path().join("read.sock"), READ_PROCEDURE);
    assert_refused_as_caller(status, &answer);
}

#[test]
fn a_bad_guard_flag_is_a_usage_error() {
    for bad in [
        &["--socket-gid", "0"][..],
        &["--socket-mode", "zz"],
        &["--allow-uid", "root"],
        &["--allow-any-uid", "--allow-uid", "1"],
    ] {
        let dir = tempfile::tempdir().unwrap();
        let mut cmd = Command::new(BIN);
        cmd.arg("serve");
        let state = dir.path().join("state");
        std::fs::create_dir(&state).unwrap();
        std::fs::set_permissions(&state, std::fs::Permissions::from_mode(0o700)).unwrap();
        cmd.arg("--socket")
            .arg(socket(dir.path()))
            .arg("--read-socket")
            .arg(dir.path().join("read.sock"))
            .arg("--state-dir")
            .arg(&state)
            .arg("--declaration")
            .arg(declaration(dir.path()));
        let out = cmd.args(bad).output().unwrap();
        assert_eq!(out.status.code(), Some(2), "{bad:?}: {out:?}");
    }
}

#[test]
fn version_is_a_command_and_answers_as_the_flag_does() {
    let word = Command::new(BIN).arg("version").output().unwrap();
    assert_eq!(word.status.code(), Some(0), "{word:?}");
    let flag = Command::new(BIN).arg("--version").output().unwrap();
    assert_eq!(flag.status.code(), Some(0), "{flag:?}");
    let (word, flag) = (
        String::from_utf8(word.stdout).unwrap(),
        String::from_utf8(flag.stdout).unwrap(),
    );
    assert!(word.starts_with("index-sqlite "), "{word}");
    assert_eq!(word, flag);
}
