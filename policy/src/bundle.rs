//! The payload (record 0019, "The payload"): a JSON object of the schema, the policies and templates by id, and the links. It is read
//! here and compiled by [`crate::Set::compile`].

use std::collections::{BTreeMap, HashSet};
use std::fmt;

use crate::json::{Json, get, parse};

/// The classes of a [`Fault`]: what part of a bundle failed. The decision test set compares the class and the id, never the text of
/// the rule, which is each engine's own.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Class {
    /// The payload is not a bundle (not a JSON object of the members of record 0019, a bad or duplicate id).
    Payload,
    /// The schema does not parse.
    Schema,
    /// A policy does not parse, or is not one static policy.
    Policy,
    /// A template does not parse, or is not one template with a slot.
    Template,
    /// A link names no template, or its values do not fill the template's slots.
    Link,
    /// A policy or a template fails strict validation against the schema.
    Validation,
    /// The bundle uses what this engine cannot do. cedar-policy has no such gap; the class is the Go engine's.
    Unsupported,
}

impl Class {
    /// The name of the class, as the test set spells it.
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Payload => "payload",
            Self::Schema => "schema",
            Self::Policy => "policy",
            Self::Template => "template",
            Self::Link => "link",
            Self::Validation => "validation",
            Self::Unsupported => "unsupported",
        }
    }
}

/// Why a bundle was refused as a whole: the class, the id of the policy, template or link at fault (or the member of the payload),
/// and the rule. It names an id or a member and never quotes the text.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Fault {
    pub class: Class,
    pub id: String,
    pub rule: &'static str,
}

impl fmt::Display for Fault {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.id.is_empty() {
            write!(f, "bundle: {}: {}", self.class.as_str(), self.rule)
        } else {
            write!(
                f,
                "bundle: {} {}: {}",
                self.class.as_str(),
                self.id,
                self.rule
            )
        }
    }
}

impl std::error::Error for Fault {}

pub(crate) fn fault<T>(class: Class, id: &str, rule: &'static str) -> Result<T, Fault> {
    Err(Fault {
        class,
        id: id.to_owned(),
        rule,
    })
}

/// An entity named by its type and id, as a link fills a slot with it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct EntityRef {
    pub ty: String,
    pub id: String,
}

/// A template link: the id of the linked policy, the template it links, and the entity of each slot (`?principal`, `?resource`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Link {
    pub id: String,
    pub template: String,
    pub values: BTreeMap<String, EntityRef>,
}

/// A payload read but not compiled.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Bundle {
    pub schema: String,
    pub policies: BTreeMap<String, String>,
    pub templates: BTreeMap<String, String>,
    pub links: Vec<Link>,
}

/// The longest id of a policy, a template or a link.
const MAX_ID: usize = 128;

/// 1 to 128 characters, a letter or a digit first, then letters, digits and `.`, `_`, `-`, `:`, `/`.
fn valid_id(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= MAX_ID
        && s.bytes().enumerate().all(|(i, c)| {
            c.is_ascii_alphanumeric() || (i > 0 && matches!(c, b'.' | b'_' | b'-' | b':' | b'/'))
        })
}

fn texts(m: &[(String, Json)], member: &str) -> Result<BTreeMap<String, String>, Fault> {
    const RULE: &str = "an object of id to text, each id of the form of record 0019";
    let Some(Json::Obj(o)) = get(m, member) else {
        return fault(Class::Payload, member, RULE);
    };
    let mut out = BTreeMap::new();
    for (id, v) in o {
        let Json::Str(text) = v else {
            return fault(Class::Payload, member, RULE);
        };
        if !valid_id(id) {
            return fault(Class::Payload, member, RULE);
        }
        out.insert(id.clone(), text.clone());
    }
    Ok(out)
}

fn link(x: &Json) -> Result<Link, Fault> {
    const SHAPE: &str = "a link has id, template and values, and no other member";
    const VALUES: &str =
        "the values of a link map ?principal and ?resource to an entity of type and id";
    let Some([Json::Str(id), Json::Str(template), Json::Obj(values)]) =
        crate::json::exactly(x, &["id", "template", "values"]).as_deref()
    else {
        return fault(Class::Payload, "links", SHAPE);
    };
    if !valid_id(id) || !valid_id(template) {
        return fault(Class::Payload, "links", SHAPE);
    }
    let mut l = Link {
        id: id.clone(),
        template: template.clone(),
        values: BTreeMap::new(),
    };
    for (slot, v) in values {
        let Some([Json::Str(ty), Json::Str(eid)]) =
            crate::json::exactly(v, &["type", "id"]).as_deref()
        else {
            return fault(Class::Payload, "links", VALUES);
        };
        if slot != "?principal" && slot != "?resource" {
            return fault(Class::Payload, "links", VALUES);
        }
        l.values.insert(
            slot.clone(),
            EntityRef {
                ty: ty.clone(),
                id: eid.clone(),
            },
        );
    }
    Ok(l)
}

impl Bundle {
    /// Reads the payload of a verified document. It checks the shape and the ids and parses no Cedar: that is
    /// [`crate::Set::compile`]'s.
    ///
    /// # Errors
    ///
    /// A [`Fault`] of the class [`Class::Payload`].
    pub fn parse(payload: &[u8]) -> Result<Self, Fault> {
        let Some(Json::Obj(m)) = parse(payload) else {
            return fault(Class::Payload, "", "not a JSON object");
        };
        if m.iter()
            .any(|(k, _)| !matches!(k.as_str(), "schema" | "policies" | "templates" | "links"))
        {
            return fault(
                Class::Payload,
                "",
                "a member that is not schema, policies, templates or links",
            );
        }
        let Some(Json::Str(schema)) = get(&m, "schema") else {
            return fault(Class::Payload, "schema", "needs the schema, a string");
        };
        if get(&m, "policies").is_none() {
            return fault(Class::Payload, "policies", "needs the policies, an object");
        }
        let policies = texts(&m, "policies")?;
        let templates = if get(&m, "templates").is_some() {
            texts(&m, "templates")?
        } else {
            BTreeMap::new()
        };
        let mut links = Vec::new();
        if let Some(v) = get(&m, "links") {
            let Json::Arr(a) = v else {
                return fault(Class::Payload, "links", "links is a list");
            };
            for x in a {
                links.push(link(x)?);
            }
        }
        // One name space for policies, templates and links, as Cedar has: the first id seen twice, in the order policies, templates
        // (each sorted), links (as listed).
        let mut seen = HashSet::new();
        for id in policies
            .keys()
            .chain(templates.keys())
            .chain(links.iter().map(|l| &l.id))
        {
            if !seen.insert(id.as_str()) {
                return fault(Class::Payload, id, "an id is used twice");
            }
        }
        Ok(Self {
            schema: schema.clone(),
            policies,
            templates,
            links,
        })
    }
}
