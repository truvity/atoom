//! The signed document (record 0019, "The signed document"): its structure and its signatures, steps 1 to 4, and the sequence, step 5.
//!
//! Every part that is base64url is decoded strictly: the alphabet of RFC 7515 section 2 and nothing else, no padding, no white space,
//! no stray bit. Nothing here reads the payload. A refusal names the step and the rule, never a value.

use std::collections::HashSet;
use std::fmt;

use aws_lc_rs::digest;
use aws_lc_rs::signature::{ECDSA_P384_SHA384_FIXED, UnparsedPublicKey};
use base64::Engine as _;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;

use crate::json::{Json, exactly, parse};

/// The largest signed document a loader reads: 3 MiB. A larger one is refused before a byte of it is parsed.
pub const MAX_DOCUMENT: usize = 3 << 20;
/// `seq` is below 2^53, so that a JSON number holds it exactly in every reader.
const SEQ_LIMIT: u64 = 1 << 53;
/// An ECDSA P-384 signature, `r` then `s`, 48 bytes each (RFC 7518, section 3.4).
const SIGNATURE_LEN: usize = 96;
/// The DER of a P-384 SubjectPublicKeyInfo up to the 97-byte uncompressed point.
const SPKI_P384_PREFIX: [u8; 23] = [
    0x30, 0x76, 0x30, 0x10, 0x06, 0x07, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x02, 0x01, 0x06, 0x05, 0x2b,
    0x81, 0x04, 0x00, 0x22, 0x03, 0x62, 0x00,
];

/// A key the host pins: the kid a signature names, the P-384 public key, and the kinds whose documents need this key's signature as
/// well (a co-signer). A key is pinned by the host's own configuration and never by a document.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Publisher {
    pub kid: String,
    /// The uncompressed point of the key (97 bytes).
    point: Vec<u8>,
    pub co_signs: Vec<String>,
}

/// A key that is not the SubjectPublicKeyInfo of a P-384 key.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct NotP384;

impl fmt::Display for NotP384 {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("not the SubjectPublicKeyInfo of a P-384 key")
    }
}

impl std::error::Error for NotP384 {}

impl Publisher {
    /// A publisher from the DER of a SubjectPublicKeyInfo of a P-384 key.
    ///
    /// # Errors
    ///
    /// [`NotP384`] for any other key or bytes.
    pub fn from_spki(
        kid: impl Into<String>,
        spki_der: &[u8],
        co_signs: Vec<String>,
    ) -> Result<Self, NotP384> {
        let point = spki_der
            .strip_prefix(&SPKI_P384_PREFIX[..])
            .filter(|p| p.len() == 97 && p[0] == 0x04)
            .ok_or(NotP384)?;
        Ok(Self {
            kid: kid.into(),
            point: point.to_vec(),
            co_signs,
        })
    }

    /// A publisher from the uncompressed point of a P-384 key (`0x04 || x || y`, 97 bytes), as `aws-lc-rs` gives it.
    ///
    /// # Errors
    ///
    /// [`NotP384`] for bytes of another length or form.
    pub fn from_point(
        kid: impl Into<String>,
        point: &[u8],
        co_signs: Vec<String>,
    ) -> Result<Self, NotP384> {
        let mut der = SPKI_P384_PREFIX.to_vec();
        der.extend_from_slice(point);
        Self::from_spki(kid, &der, co_signs)
    }
}

/// Why a document was refused: the step and the rule.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Refused {
    pub step: u8,
    pub rule: &'static str,
}

impl fmt::Display for Refused {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "signed document: step {}: {}", self.step, self.rule)
    }
}

impl std::error::Error for Refused {}

fn refuse<T>(step: u8, rule: &'static str) -> Result<T, Refused> {
    Err(Refused { step, rule })
}

/// A document whose signatures verified under the pinned keys.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Verified {
    pub seq: u64,
    /// The signed bytes, as they were signed.
    pub payload: Vec<u8>,
    /// The SHA-384 of the payload.
    pub digest: [u8; 48],
    /// The kids that signed it, in the order of the signatures.
    pub kids: Vec<String>,
}

impl Verified {
    /// The digest in lower-case hex, as an audit record and `sha384sum` show it.
    #[must_use]
    pub fn digest_hex(&self) -> String {
        hex(&self.digest)
    }
}

fn b64(s: &str) -> Option<Vec<u8>> {
    URL_SAFE_NO_PAD.decode(s.as_bytes()).ok()
}

struct Signature<'a> {
    protected: &'a str,
    signature: &'a str,
    kid: String,
    seq: u64,
}

/// Verifies the document `doc` of the kind `kind` under the pinned `publishers`: steps 1 to 4 of record 0019. The sequence (step 5) is
/// [`sequence`]'s, and the payload is [`crate::Bundle::parse`]'s.
///
/// # Errors
///
/// [`Refused`] with the step at which the document failed.
pub fn verify(doc: &[u8], kind: &str, publishers: &[Publisher]) -> Result<Verified, Refused> {
    if doc.len() > MAX_DOCUMENT {
        return refuse(1, "the document is larger than the bound");
    }
    // Step 1: structure. The general JSON serialisation; the compact and the flattened ones, an unprotected `header` and any other
    // member are all refused here.
    let Some(json) = parse(doc) else {
        return refuse(1, "not a JSON object in the general serialisation");
    };
    let Some([Json::Str(payload), Json::Arr(sigs)]) =
        exactly(&json, &["payload", "signatures"]).as_deref()
    else {
        return refuse(1, "needs a payload and signatures, and no other member");
    };
    if sigs.is_empty() {
        return refuse(1, "needs at least one signature");
    }
    let mut parsed = Vec::with_capacity(sigs.len());
    for s in sigs {
        let Some([Json::Str(protected), Json::Str(signature)]) =
            exactly(s, &["protected", "signature"]).as_deref()
        else {
            return refuse(
                1,
                "a signature has protected and signature, and no other member",
            );
        };
        parsed.push((protected.as_str(), signature.as_str()));
    }

    // Step 2: headers.
    let mut signatures = Vec::with_capacity(parsed.len());
    for (protected, signature) in parsed {
        let Some(header) = b64(protected).and_then(|b| parse(&b)) else {
            return refuse(2, "the protected header is not base64url JSON");
        };
        let Some(
            [
                Json::Str(alg),
                Json::Str(kid),
                Json::Num(seq),
                Json::Str(named),
            ],
        ) = exactly(&header, &["alg", "kid", "seq", "kind"]).as_deref()
        else {
            return refuse(
                2,
                "the protected header needs alg, kid, seq and kind, and no other member",
            );
        };
        if alg != "ES384" {
            return refuse(2, "alg must be ES384");
        }
        // The signed header names the kind, so a document of one kind cannot be served as another's.
        if named != kind {
            return refuse(2, "kind is not the kind of the document asked for");
        }
        let Some(seq) = seq.as_u64().filter(|s| (1..SEQ_LIMIT).contains(s)) else {
            return refuse(2, "seq must be a positive integer below 2^53");
        };
        signatures.push(Signature {
            protected,
            signature,
            kid: kid.clone(),
            seq,
        });
    }
    let seq = signatures[0].seq;
    if signatures.iter().any(|s| s.seq != seq) {
        return refuse(2, "the signatures carry different seq");
    }

    // Step 3: keys. A kid names a pinned key; nothing in the document names or carries a key.
    let mut used = HashSet::new();
    let mut keys = Vec::with_capacity(signatures.len());
    for s in &signatures {
        let Some(p) = publishers.iter().find(|p| p.kid == s.kid) else {
            return refuse(3, "a kid is not pinned");
        };
        if !used.insert(s.kid.as_str()) {
            return refuse(3, "a kid is used twice");
        }
        keys.push(p);
    }

    // Step 4: signatures, over the bytes as they were received.
    for (s, p) in signatures.iter().zip(&keys) {
        let Some(sig) = b64(s.signature).filter(|b| b.len() == SIGNATURE_LEN) else {
            return refuse(4, "a signature is not 96 bytes of r then s");
        };
        let input = format!("{}.{}", s.protected, payload);
        if UnparsedPublicKey::new(&ECDSA_P384_SHA384_FIXED, &p.point)
            .verify(input.as_bytes(), &sig)
            .is_err()
        {
            return refuse(4, "a signature does not verify");
        }
    }
    // One publisher key without co-signs, and every key whose co-signs name this kind.
    if !keys.iter().any(|p| p.co_signs.is_empty()) {
        return refuse(4, "no publisher key signed it");
    }
    if publishers
        .iter()
        .any(|p| p.co_signs.iter().any(|k| k == kind) && !used.contains(p.kid.as_str()))
    {
        return refuse(4, "a co-signer's signature is missing");
    }
    let Some(payload) = b64(payload) else {
        return refuse(4, "the payload is not base64url");
    };
    let mut d = [0u8; 48];
    d.copy_from_slice(digest::digest(&digest::SHA384, &payload).as_ref());
    Ok(Verified {
        seq,
        payload,
        digest: d,
        kids: signatures.into_iter().map(|s| s.kid).collect(),
    })
}

/// What the sequence number says about a verified document against the one in force (step 5).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Sequence {
    /// A higher `seq` than the one in force, or nothing in force: it replaces it.
    Newer,
    /// The same `seq` and the same payload: nothing changes.
    Same,
    /// The same `seq` with another payload: refused, two documents must not share a number.
    Conflict,
    /// A lower `seq`: refused, an older document must not come back.
    Older,
}

impl fmt::Display for Sequence {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(match self {
            Self::Newer => "newer",
            Self::Same => "same",
            Self::Conflict => "conflict",
            Self::Older => "older",
        })
    }
}

/// Step 5. `in_force` is the `seq` and the payload digest of the document in force, if there is one. A loader that starts has nothing
/// in force and takes whatever `seq` it is given.
#[must_use]
pub fn sequence(in_force: Option<(u64, &[u8; 48])>, v: &Verified) -> Sequence {
    match in_force {
        None => Sequence::Newer,
        Some((seq, digest)) => match v.seq.cmp(&seq) {
            std::cmp::Ordering::Greater => Sequence::Newer,
            std::cmp::Ordering::Equal if v.digest == *digest => Sequence::Same,
            std::cmp::Ordering::Equal => Sequence::Conflict,
            std::cmp::Ordering::Less => Sequence::Older,
        },
    }
}

/// Bytes in lower-case hex.
#[must_use]
pub fn hex(d: &[u8]) -> String {
    use std::fmt::Write as _;
    d.iter()
        .fold(String::with_capacity(d.len() * 2), |mut s, b| {
            let _ = write!(s, "{b:02x}");
            s
        })
}
