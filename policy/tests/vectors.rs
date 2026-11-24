//! The decision test set (record 0019): the same files the Go package runs (vectors_test.go). cedar-policy is the reference engine, so
//! every case is held to its `expect`; `expect_go` is the Go engine's.

use std::collections::BTreeMap;

use atoom_policy::{Bundle, Publisher, Refused, Set, hex, sequence, verify};
use base64::Engine as _;
use base64::engine::general_purpose::STANDARD;
use serde_json::Value;

fn read(file: &str) -> Value {
    let path = format!("{}/testdata/{file}", env!("CARGO_MANIFEST_DIR"));
    serde_json::from_slice(&std::fs::read(&path).expect("read the vectors"))
        .expect("parse the vectors")
}

fn s(v: &Value) -> &str {
    v.as_str().expect("a string")
}

fn digest48(h: &str) -> [u8; 48] {
    let mut out = [0u8; 48];
    assert_eq!(h.len(), 96, "not a SHA-384: {h}");
    for (i, b) in out.iter_mut().enumerate() {
        *b = u8::from_str_radix(&h[2 * i..2 * i + 2], 16).expect("hex");
    }
    out
}

#[test]
fn signed_document_vectors() {
    let v = read("signed-document.json");
    let kind = s(&v["kind"]);
    let mut sets: BTreeMap<String, Vec<Publisher>> = BTreeMap::new();
    for (name, ps) in v["publisher_sets"].as_object().expect("sets") {
        for p in ps.as_array().expect("a set") {
            let der = STANDARD.decode(s(&p["spki_der"])).expect("base64");
            let co: Vec<String> = p["co_signs"]
                .as_array()
                .expect("co_signs")
                .iter()
                .map(|x| s(x).to_owned())
                .collect();
            sets.entry(name.clone())
                .or_default()
                .push(Publisher::from_spki(s(&p["kid"]), &der, co).expect("a P-384 key"));
        }
    }
    let cases = v["cases"].as_array().expect("cases");
    assert!(cases.len() >= 60, "only {} cases", cases.len());
    let mut failures = Vec::new();
    for c in cases {
        let name = s(&c["name"]);
        let doc = match c.get("document_base64") {
            Some(b) => STANDARD.decode(s(b)).expect("base64"),
            None => s(&c["document"]).as_bytes().to_vec(),
        };
        let got = verify(&doc, kind, &sets[s(&c["publishers"])]);
        match (&c.get("read"), got) {
            (Some(r), Ok(got)) => {
                if got.seq != r["seq"].as_u64().expect("seq")
                    || got.digest_hex() != s(&r["sha384"])
                    || got.payload != s(&r["payload"]).as_bytes()
                {
                    failures.push(format!("{name}: read {got:?}"));
                }
            }
            (Some(_), Err(e)) => failures.push(format!("{name}: refused: {e}")),
            (None, Ok(_)) => failures.push(format!("{name}: read, want a refusal")),
            (None, Err(Refused { step, rule })) => {
                let want = c["refused_step"].as_u64().expect("refused_step");
                if u64::from(step) != want {
                    failures.push(format!(
                        "{name}: refused at step {step} ({rule}), want {want}"
                    ));
                }
            }
        }
    }
    for c in v["sequence"].as_array().expect("sequence") {
        let doc = atoom_policy::Verified {
            seq: c["document"]["seq"].as_u64().expect("seq"),
            payload: Vec::new(),
            digest: digest48(s(&c["document"]["sha384"])),
            kids: Vec::new(),
        };
        let held = c["in_force"]
            .as_object()
            .map(|h| (h["seq"].as_u64().expect("seq"), digest48(s(&h["sha384"]))));
        let got = sequence(held.as_ref().map(|(q, d)| (*q, d)), &doc);
        if got.to_string() != s(&c["verdict"]) {
            failures.push(format!("sequence {}: {got}", s(&c["name"])));
        }
    }
    assert!(failures.is_empty(), "{}", failures.join("\n"));
    assert_eq!(hex(&[0, 255]), "00ff");
}

fn payload_of(c: &Value) -> Vec<u8> {
    match c.get("payload_text") {
        Some(t) => s(t).as_bytes().to_vec(),
        None => serde_json::to_vec(&c["bundle"]).expect("json"),
    }
}

fn compile(payload: &[u8]) -> Result<Set, atoom_policy::Fault> {
    Set::compile(&Bundle::parse(payload)?)
}

#[test]
fn bundle_vectors() {
    let v = read("bundles.json");
    let mut failures = Vec::new();
    for c in v["cases"].as_array().expect("cases") {
        let name = s(&c["name"]);
        let want = &c["expect"];
        match (compile(&payload_of(c)), want.get("refused")) {
            (Ok(set), None) => {
                let ids: Vec<&str> = want["loaded"]
                    .as_array()
                    .expect("loaded")
                    .iter()
                    .map(s)
                    .collect();
                if set.policy_ids() != ids.as_slice() {
                    failures.push(format!(
                        "{name}: loaded {:?}, want {ids:?}",
                        set.policy_ids()
                    ));
                }
            }
            (Ok(_), Some(r)) => failures.push(format!("{name}: loaded, want the fault {r}")),
            (Err(f), None) => failures.push(format!("{name}: refused: {f}")),
            (Err(f), Some(r)) => {
                if f.class.as_str() != s(&r["class"]) || f.id != s(&r["id"]) {
                    failures.push(format!(
                        "{name}: fault {} {:?} ({}), want {r}",
                        f.class.as_str(),
                        f.id,
                        f.rule
                    ));
                }
            }
        }
    }
    assert!(failures.is_empty(), "{}", failures.join("\n"));
}

fn uid_of(v: &Value) -> atoom_policy::cedar_policy::EntityUid {
    atoom_policy::uid(s(&v["type"]), s(&v["id"])).expect("an entity type")
}

#[test]
fn decision_vectors() {
    let v = read("decisions.json");
    let mut sets = BTreeMap::new();
    for (name, b) in v["bundles"].as_object().expect("bundles") {
        let set = compile(&serde_json::to_vec(b).expect("json"))
            .unwrap_or_else(|f| panic!("bundle {name}: {f}"));
        sets.insert(name.clone(), set);
    }
    let mut failures = Vec::new();
    let cases = v["cases"].as_array().expect("cases");
    for c in cases {
        let name = s(&c["name"]);
        let set = &sets[s(&c["bundle"])];
        let action = uid_of(&c["action"]);
        let want = &c["expect"];
        let got = set.entities_from_json(c["entities"].clone()).and_then(|e| {
            let ctx = set.context_from_json(&action, c["context"].clone())?;
            set.decide(
                uid_of(&c["principal"]),
                action.clone(),
                uid_of(&c["resource"]),
                ctx,
                &e,
            )
        });
        let misfit = want.get("misfit").and_then(Value::as_bool).unwrap_or(false);
        match got {
            Err(m) if !misfit => failures.push(format!("{name}: misfit: {m}")),
            Err(_) => {}
            Ok(d) if misfit => failures.push(format!("{name}: decided {d:?}, want a misfit")),
            Ok(d) => {
                let ids = |k: &str| -> Vec<String> {
                    want[k]
                        .as_array()
                        .expect("ids")
                        .iter()
                        .map(|x| s(x).to_owned())
                        .collect()
                };
                let decision = if d.allow { "allow" } else { "deny" };
                if decision != s(&want["decision"])
                    || d.reasons != ids("reasons")
                    || d.errors != ids("errors")
                {
                    failures.push(format!(
                        "{name}: got {decision} {:?} {:?}, want {want}",
                        d.reasons, d.errors
                    ));
                }
                if d.digest != set.digest() {
                    failures.push(format!(
                        "{name}: the decision does not carry the digest of its set"
                    ));
                }
            }
        }
    }
    assert!(cases.len() >= 25, "only {} decision cases", cases.len());
    assert!(failures.is_empty(), "{}", failures.join("\n"));
}
