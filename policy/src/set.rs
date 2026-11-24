//! A compiled set (record 0019, "Compiling" and "Deciding"): the schema and the policies checked against it in strict mode, and one
//! decision over the entities the caller gives, with no I/O.

use std::collections::HashMap;
use std::fmt;
use std::str::FromStr as _;

use cedar_policy::{
    Authorizer, Context, Decision as CedarDecision, Entities, Entity, EntityId, EntityTypeName,
    EntityUid, Policy, PolicyId, PolicySet, Request, Schema, SlotId, Template, ValidationMode,
    Validator,
};

use crate::bundle::{Bundle, Class, Fault, fault};
use crate::document::Verified;

/// A compiled policy set. It never changes; a reload builds a new one.
pub struct Set {
    digest: String,
    seq: u64,
    schema: Schema,
    policies: PolicySet,
    ids: Vec<String>,
    authorizer: Authorizer,
}

impl fmt::Debug for Set {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Set")
            .field("seq", &self.seq)
            .field("digest", &self.digest)
            .finish_non_exhaustive()
    }
}

/// What the set decided: allow or deny, the policies that determined it (sorted), the policies that errored (sorted; an erroring
/// policy counts as neither permit nor forbid), and the digest and seq of the set, for the audit record.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Decision {
    pub allow: bool,
    pub reasons: Vec<String>,
    pub errors: Vec<String>,
    pub digest: String,
    pub seq: u64,
}

/// A request or an entity that does not fit the schema. The caller treats it as a denial.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Misfit {
    pub rule: &'static str,
}

impl fmt::Display for Misfit {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "the request does not fit the schema: {}", self.rule)
    }
}

impl std::error::Error for Misfit {}

fn misfit<T>(rule: &'static str) -> Result<T, Misfit> {
    Err(Misfit { rule })
}

/// Whether an entity type is the action type of a name space (`Action`, `Ns::Action`).
fn is_action_type(ty: &str) -> bool {
    ty == "Action" || ty.ends_with("::Action")
}

/// An entity from a type name and an id, the id taken verbatim.
///
/// # Errors
///
/// `None` when `ty` is not an entity type name.
#[must_use]
pub fn uid(ty: &str, id: &str) -> Option<EntityUid> {
    let ty = EntityTypeName::from_str(ty).ok()?;
    Some(EntityUid::from_type_name_and_id(ty, EntityId::new(id)))
}

impl Set {
    /// Reads the payload of a verified document and compiles it ([`Bundle::parse`], then [`Set::compile`]), with the document's digest
    /// and seq.
    ///
    /// # Errors
    ///
    /// The [`Fault`] of the first check that fails.
    pub fn from_verified(v: &Verified) -> Result<Self, Fault> {
        let b = Bundle::parse(&v.payload)?;
        let mut s = Self::compile(&b)?;
        s.digest = v.digest_hex();
        s.seq = v.seq;
        Ok(s)
    }

    /// Parses the schema, every policy and template, links the templates, and validates the set against the schema in strict mode.
    /// Any fault refuses the whole bundle. The order of the checks is the record's: the schema, the policies (by id), the templates
    /// (by id), the links (as listed), then validation, which names the lowest id among those that fail.
    ///
    /// # Errors
    ///
    /// The [`Fault`] of the first check that fails.
    pub fn compile(b: &Bundle) -> Result<Self, Fault> {
        let Ok((schema, _warnings)) = Schema::from_cedarschema_str(&b.schema) else {
            return fault(Class::Schema, "", "the schema does not parse");
        };
        let mut set = PolicySet::new();
        for (id, text) in &b.policies {
            let Ok(p) = Policy::parse(Some(PolicyId::new(id)), text) else {
                return fault(
                    Class::Policy,
                    id,
                    "the policy does not parse as one static policy",
                );
            };
            if set.add(p).is_err() {
                return fault(Class::Payload, id, "an id is used twice");
            }
        }
        for (id, text) in &b.templates {
            let Ok(t) = Template::parse(Some(PolicyId::new(id)), text) else {
                return fault(
                    Class::Template,
                    id,
                    "the template does not parse as one template",
                );
            };
            if t.slots().next().is_none() {
                return fault(Class::Template, id, "a template has a slot");
            }
            if set.add_template(t).is_err() {
                return fault(Class::Payload, id, "an id is used twice");
            }
        }
        for l in &b.links {
            if !b.templates.contains_key(&l.template) {
                return fault(Class::Link, &l.id, "the link names no template");
            }
            let mut vals = HashMap::new();
            for (slot, e) in &l.values {
                let slot = if slot == "?principal" {
                    SlotId::principal()
                } else {
                    SlotId::resource()
                };
                let Some(u) = uid(&e.ty, &e.id) else {
                    return fault(Class::Link, &l.id, "a value of the link is not an entity");
                };
                vals.insert(slot, u);
            }
            if set
                .link(PolicyId::new(&l.template), PolicyId::new(&l.id), vals)
                .is_err()
            {
                return fault(
                    Class::Link,
                    &l.id,
                    "the values do not fill the slots of the template",
                );
            }
        }
        let result = Validator::new(schema.clone()).validate(&set, ValidationMode::Strict);
        if let Some(id) = result
            .validation_errors()
            .map(|e| e.policy_id().to_string())
            .min()
        {
            return fault(
                Class::Validation,
                &id,
                "the policy fails strict validation against the schema",
            );
        }
        let mut ids: Vec<String> = set.policies().map(|p| p.id().to_string()).collect();
        ids.sort();
        Ok(Self {
            digest: String::new(),
            seq: 0,
            schema,
            policies: set,
            ids,
            authorizer: Authorizer::new(),
        })
    }

    /// The SHA-384 of the payload the set was built from, in lower-case hex: what each decision is audited with.
    #[must_use]
    pub fn digest(&self) -> &str {
        &self.digest
    }

    /// The seq of the document the set was built from.
    #[must_use]
    pub fn seq(&self) -> u64 {
        self.seq
    }

    /// The ids of the policies of the set, static and linked, sorted.
    #[must_use]
    pub fn policy_ids(&self) -> &[String] {
        &self.ids
    }

    /// The schema of the set, for a host that builds its entities and context with it.
    #[must_use]
    pub fn schema(&self) -> &Schema {
        &self.schema
    }

    /// The entities of one request, from the JSON entity format of Cedar, checked against the schema, with the action entities of the
    /// schema added. The caller gives no action entity.
    ///
    /// # Errors
    ///
    /// [`Misfit`] when an entity does not fit the schema or is an action.
    pub fn entities_from_json(&self, json: serde_json::Value) -> Result<Entities, Misfit> {
        let gives_action = json.as_array().is_some_and(|a| {
            a.iter().any(|e| {
                e.pointer("/uid/type")
                    .or_else(|| e.pointer("/uid/__entity/type"))
                    .and_then(serde_json::Value::as_str)
                    .is_some_and(is_action_type)
            })
        });
        if gives_action {
            return misfit("the caller gives an action entity");
        }
        Entities::from_json_value(json, Some(&self.schema)).or_else(|_| misfit("the entities"))
    }

    /// The entities of one request, checked against the schema, with the action entities of the schema added.
    ///
    /// # Errors
    ///
    /// [`Misfit`] when an entity does not fit the schema or is an action.
    pub fn entities(&self, entities: impl IntoIterator<Item = Entity>) -> Result<Entities, Misfit> {
        let given: Vec<Entity> = entities.into_iter().collect();
        if given
            .iter()
            .any(|e| is_action_type(&e.uid().type_name().to_string()))
        {
            return misfit("the caller gives an action entity");
        }
        let Ok(actions) = self.schema.action_entities() else {
            return misfit("the actions of the schema");
        };
        let given = Entities::from_entities(given, Some(&self.schema))
            .or_else(|_| misfit("the entities"))?;
        given
            .add_entities(actions, None)
            .or_else(|_| misfit("the entities"))
    }

    /// The context of one request for `action`, from the JSON form of Cedar, checked against the schema.
    ///
    /// # Errors
    ///
    /// [`Misfit`] when the context does not fit the schema of the action, or the action is not in the schema.
    pub fn context_from_json(
        &self,
        action: &EntityUid,
        json: serde_json::Value,
    ) -> Result<Context, Misfit> {
        Context::from_json_value(json, Some((&self.schema, action)))
            .or_else(|_| misfit("the context"))
    }

    /// Evaluates one request. The request is checked against the schema first; the entities are those of [`Set::entities`] or
    /// [`Set::entities_from_json`]. It does no I/O.
    ///
    /// # Errors
    ///
    /// [`Misfit`] when the request does not fit the schema: a denial.
    pub fn decide(
        &self,
        principal: EntityUid,
        action: EntityUid,
        resource: EntityUid,
        context: Context,
        entities: &Entities,
    ) -> Result<Decision, Misfit> {
        let Ok(req) = Request::new(principal, action, resource, context, Some(&self.schema)) else {
            return misfit("the request");
        };
        let resp = self
            .authorizer
            .is_authorized(&req, &self.policies, entities);
        let mut reasons: Vec<String> = resp
            .diagnostics()
            .reason()
            .map(ToString::to_string)
            .collect();
        let mut errors: Vec<String> = resp
            .diagnostics()
            .errors()
            .map(|e| match e {
                cedar_policy::AuthorizationError::PolicyEvaluationError(p) => {
                    p.policy_id().to_string()
                }
            })
            .collect();
        reasons.sort();
        errors.sort();
        Ok(Decision {
            allow: resp.decision() == CedarDecision::Allow,
            reasons,
            errors,
            digest: self.digest.clone(),
            seq: self.seq,
        })
    }
}
