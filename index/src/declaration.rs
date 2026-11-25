//! The declaration of an index: the log kinds that have an index and the columns beyond the fixed ones, as the host gives it to the
//! driver it starts (record 0025). Its form is the proto message `atoom.index.v1alpha2.Declaration`; its file is the message's JSON form
//! (the proto JSON mapping), the `--declaration FILE` of a thin main. [`Schema`] is a declaration after its checks, the same for every
//! driver: `index/sqlite`, and the drivers to come, read it through this module, and the Go twin (`index.ParseDeclaration`) holds the
//! same rules over the same test vectors (`index/testdata/declarations`).

use std::fmt;

use crate::atoom::index::v1alpha2::{ColumnDeclaration, ColumnType, Declaration};

/// The most declared columns.
pub const MAX_COLUMNS: usize = 32;
/// The most declared log kinds.
pub const MAX_KINDS: usize = 32;
/// The most bytes of a log kind's name.
pub const MAX_KIND: usize = 128;
/// The names of the fixed columns and fields of a row and a log, which no declared column may take.
pub const FIXED: [&str; 10] = [
    "scope", "kind", "log", "tenant", "hash", "seq", "time_ms", "entry_id", "entry", "id",
];

/// Why a declaration is refused: every problem, each naming the field at fault and never a value of the file beyond a column's
/// position.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DeclarationError {
    /// The problems, in the order of the declaration.
    pub problems: Vec<String>,
}

impl fmt::Display for DeclarationError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "the index declaration is refused: {}",
            self.problems.join("; ")
        )
    }
}

impl std::error::Error for DeclarationError {}

/// The type of a declared column.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Type {
    /// A string of at most 1024 bytes, no NUL.
    Text,
    /// A signed 64-bit integer.
    Int,
}

/// A declared column after its checks.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Column {
    /// The name, `[a-z][a-z0-9_]{0,31}`.
    pub name: String,
    /// The type.
    pub ty: Type,
    /// Reached by a word search.
    pub words: bool,
    /// Reached by an exact match.
    pub filter: bool,
    /// A row without it is refused.
    pub required: bool,
}

/// A declaration after its checks: the kinds sorted, the columns sorted by name.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Schema {
    kinds: Vec<String>,
    columns: Vec<Column>,
}

fn column_name_ok(n: &str) -> bool {
    let b = n.as_bytes();
    !b.is_empty()
        && b.len() <= 32
        && b[0].is_ascii_lowercase()
        && b.iter()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || *c == b'_')
}

fn kind_ok(k: &str) -> bool {
    !k.is_empty() && k.len() <= MAX_KIND && k.chars().all(|c| !c.is_control())
}

impl Schema {
    /// Reads the JSON form of a declaration and checks it.
    ///
    /// # Errors
    ///
    /// A file that is not the JSON of a declaration, or a declaration that breaks a rule.
    pub fn parse_json(text: &str) -> Result<Self, DeclarationError> {
        let d: Declaration = serde_json::from_str(text).map_err(|e| DeclarationError {
            problems: vec![format!(
                "file: not the JSON of a declaration (line {}, column {})",
                e.line(),
                e.column()
            )],
        })?;
        Self::new(&d)
    }

    /// Checks a declaration: at least one kind and at most [`MAX_KINDS`], each named, none twice; at most [`MAX_COLUMNS`] columns,
    /// each with a name of `[a-z][a-z0-9_]{0,31}` that is not a fixed one ([`FIXED`]) and not taken twice, a type of text or int, and
    /// words only on a text column.
    ///
    /// # Errors
    ///
    /// Every rule it breaks.
    pub fn new(d: &Declaration) -> Result<Self, DeclarationError> {
        let mut problems = Vec::new();
        if d.kinds.is_empty() || d.kinds.len() > MAX_KINDS {
            problems.push(format!("kinds: from 1 to {MAX_KINDS}"));
        }
        let mut kinds: Vec<String> = Vec::new();
        for (i, k) in d.kinds.iter().enumerate() {
            if !kind_ok(k) {
                problems.push(format!(
                    "kinds[{i}]: 1 to {MAX_KIND} bytes, no control character"
                ));
            } else if kinds.contains(k) {
                problems.push(format!("kinds[{i}]: declared twice"));
            } else {
                kinds.push(k.clone());
            }
        }
        if d.columns.len() > MAX_COLUMNS {
            problems.push(format!("columns: at most {MAX_COLUMNS}"));
        }
        let mut columns: Vec<Column> = Vec::new();
        for (i, c) in d.columns.iter().enumerate() {
            match Self::column(i, c) {
                Ok(col) if columns.iter().any(|o| o.name == col.name) => {
                    problems.push(format!("columns[{i}].name: declared twice"));
                }
                Ok(col) => columns.push(col),
                Err(p) => problems.extend(p),
            }
        }
        if !problems.is_empty() {
            return Err(DeclarationError { problems });
        }
        kinds.sort();
        columns.sort_by(|a, b| a.name.cmp(&b.name));
        Ok(Self { kinds, columns })
    }

    fn column(i: usize, c: &ColumnDeclaration) -> Result<Column, Vec<String>> {
        let mut p = Vec::new();
        if !column_name_ok(&c.name) {
            p.push(format!("columns[{i}].name: [a-z][a-z0-9_]{{0,31}}"));
        } else if FIXED.contains(&c.name.as_str()) {
            p.push(format!("columns[{i}].name: the name of a fixed column"));
        }
        let ty = match c.r#type.as_known() {
            Some(ColumnType::COLUMN_TYPE_TEXT) => Some(Type::Text),
            Some(ColumnType::COLUMN_TYPE_INT) => Some(Type::Int),
            _ => {
                p.push(format!(
                    "columns[{i}].type: COLUMN_TYPE_TEXT or COLUMN_TYPE_INT"
                ));
                None
            }
        };
        if c.words && ty == Some(Type::Int) {
            p.push(format!(
                "columns[{i}].words: only a text column is reached by words"
            ));
        }
        match ty {
            Some(ty) if p.is_empty() => Ok(Column {
                name: c.name.clone(),
                ty,
                words: c.words,
                filter: c.filter,
                required: c.required,
            }),
            _ => Err(p),
        }
    }

    /// The declared kinds, sorted.
    #[must_use]
    pub fn kinds(&self) -> &[String] {
        &self.kinds
    }

    /// Whether `kind` has an index.
    #[must_use]
    pub fn has_kind(&self, kind: &str) -> bool {
        self.kinds
            .binary_search_by(|k| k.as_str().cmp(kind))
            .is_ok()
    }

    /// The declared columns, sorted by name.
    #[must_use]
    pub fn columns(&self) -> &[Column] {
        &self.columns
    }

    /// The declared column `name`.
    #[must_use]
    pub fn column_named(&self, name: &str) -> Option<&Column> {
        self.columns
            .binary_search_by(|c| c.name.as_str().cmp(name))
            .ok()
            .map(|i| &self.columns[i])
    }

    /// The word columns, in the order of [`Schema::columns`].
    pub fn word_columns(&self) -> impl Iterator<Item = &Column> {
        self.columns.iter().filter(|c| c.words)
    }

    /// The canonical text of the schema: one line per kind and per column, in their order. Two declarations that check to the same
    /// schema have the same text, so a driver keeps it beside its data and refuses data made under another.
    #[must_use]
    pub fn canonical(&self) -> String {
        let mut s = String::from("atoom.index.v1alpha2.Declaration\n");
        for k in &self.kinds {
            s.push_str(&format!("kind {k:?}\n"));
        }
        for c in &self.columns {
            s.push_str(&format!(
                "column {} {} words={} filter={} required={}\n",
                c.name,
                match c.ty {
                    Type::Text => "text",
                    Type::Int => "int",
                },
                c.words,
                c.filter,
                c.required
            ));
        }
        s
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn vectors(dir: &str) -> Vec<(String, String)> {
        let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("testdata/declarations")
            .join(dir);
        let mut out: Vec<_> = std::fs::read_dir(&root)
            .unwrap()
            .map(|e| e.unwrap().path())
            .filter(|p| p.extension().is_some_and(|x| x == "json"))
            .map(|p| {
                (
                    p.display().to_string(),
                    std::fs::read_to_string(&p).unwrap(),
                )
            })
            .collect();
        out.sort();
        assert!(!out.is_empty(), "no vector in {}", root.display());
        out
    }

    #[test]
    fn the_good_vectors_are_accepted() {
        for (name, text) in vectors("good") {
            let s = Schema::parse_json(&text).unwrap_or_else(|e| panic!("{name}: {e}"));
            assert!(!s.kinds().is_empty(), "{name}");
        }
    }

    #[test]
    fn the_bad_vectors_are_refused() {
        for (name, text) in vectors("bad") {
            assert!(Schema::parse_json(&text).is_err(), "{name} was accepted");
        }
    }

    #[test]
    fn the_canonical_text_ignores_the_order() {
        let a = Schema::parse_json(
            r#"{"kinds":["b","a"],"columns":[{"name":"y","type":"COLUMN_TYPE_INT","filter":true},{"name":"x","type":"COLUMN_TYPE_TEXT","words":true}]}"#,
        )
        .unwrap();
        let b = Schema::parse_json(
            r#"{"kinds":["a","b"],"columns":[{"name":"x","type":"COLUMN_TYPE_TEXT","words":true},{"name":"y","type":"COLUMN_TYPE_INT","filter":true}]}"#,
        )
        .unwrap();
        assert_eq!(a.canonical(), b.canonical());
        assert!(a.has_kind("a") && !a.has_kind("c"));
        assert_eq!(a.column_named("y").map(|c| c.ty), Some(Type::Int));
        assert_eq!(
            a.word_columns()
                .map(|c| c.name.as_str())
                .collect::<Vec<_>>(),
            ["x"]
        );
    }
}
