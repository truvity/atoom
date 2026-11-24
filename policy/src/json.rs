//! JSON with the two rules `serde_json` does not have, as the Go loader reads it: a duplicate member is an error at any depth, and a
//! value nested deeper than [`MAX_DEPTH`] is an error. Bytes that are not UTF-8 and a lone surrogate escape are refused by
//! `serde_json` itself.

use std::collections::HashSet;
use std::fmt;

use serde::de::{self, Deserialize, Deserializer, MapAccess, SeqAccess, Visitor};

/// The deepest nesting that is read: the top object is at depth 0 (the bound of the Go reader).
const MAX_DEPTH: usize = 64;

/// A JSON value. Objects keep their members in order.
pub(crate) enum Json {
    Null,
    Bool,
    Num(serde_json::Number),
    Str(String),
    Arr(Vec<Json>),
    Obj(Vec<(String, Json)>),
}

impl<'de> Deserialize<'de> for Json {
    fn deserialize<D: Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
        struct V;
        impl<'de> Visitor<'de> for V {
            type Value = Json;
            fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
                f.write_str("JSON")
            }
            fn visit_unit<E: de::Error>(self) -> Result<Json, E> {
                Ok(Json::Null)
            }
            fn visit_bool<E: de::Error>(self, _: bool) -> Result<Json, E> {
                Ok(Json::Bool)
            }
            fn visit_i64<E: de::Error>(self, v: i64) -> Result<Json, E> {
                Ok(Json::Num(v.into()))
            }
            fn visit_u64<E: de::Error>(self, v: u64) -> Result<Json, E> {
                Ok(Json::Num(v.into()))
            }
            fn visit_f64<E: de::Error>(self, v: f64) -> Result<Json, E> {
                serde_json::Number::from_f64(v)
                    .map(Json::Num)
                    .ok_or_else(|| E::custom("number"))
            }
            fn visit_str<E: de::Error>(self, v: &str) -> Result<Json, E> {
                Ok(Json::Str(v.to_owned()))
            }
            fn visit_string<E: de::Error>(self, v: String) -> Result<Json, E> {
                Ok(Json::Str(v))
            }
            fn visit_seq<A: SeqAccess<'de>>(self, mut a: A) -> Result<Json, A::Error> {
                let mut v = Vec::new();
                while let Some(x) = a.next_element()? {
                    v.push(x);
                }
                Ok(Json::Arr(v))
            }
            fn visit_map<A: MapAccess<'de>>(self, mut a: A) -> Result<Json, A::Error> {
                let mut seen = HashSet::new();
                let mut v = Vec::new();
                while let Some((k, x)) = a.next_entry::<String, Json>()? {
                    if !seen.insert(k.clone()) {
                        return Err(de::Error::custom("duplicate key"));
                    }
                    v.push((k, x));
                }
                Ok(Json::Obj(v))
            }
        }
        d.deserialize_any(V)
    }
}

fn depth_ok(j: &Json, depth: usize) -> bool {
    if depth > MAX_DEPTH {
        return false;
    }
    match j {
        Json::Arr(a) => a.iter().all(|x| depth_ok(x, depth + 1)),
        Json::Obj(m) => m.iter().all(|(_, x)| depth_ok(x, depth + 1)),
        _ => true,
    }
}

/// One JSON object and nothing after it, or nothing.
pub(crate) fn parse(bytes: &[u8]) -> Option<Json> {
    let j: Json = serde_json::from_slice(bytes).ok()?;
    (matches!(j, Json::Obj(_)) && depth_ok(&j, 0)).then_some(j)
}

/// The members of an object, when its keys are exactly `keys` (any order).
pub(crate) fn exactly<'a>(j: &'a Json, keys: &[&str]) -> Option<Vec<&'a Json>> {
    let Json::Obj(m) = j else { return None };
    if m.len() != keys.len() {
        return None;
    }
    keys.iter()
        .map(|k| m.iter().find(|(name, _)| name == k).map(|(_, v)| v))
        .collect()
}

/// The member `key` of an object.
pub(crate) fn get<'a>(m: &'a [(String, Json)], key: &str) -> Option<&'a Json> {
    m.iter().find(|(k, _)| k == key).map(|(_, v)| v)
}
