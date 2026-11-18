//! Types every atoom port and driver speaks.
//!
//! This crate is a placeholder: step 0 of docs/plan.md sets up the build and the release flow on an empty library, and step 1
//! moves the common types (`Refusal`, `IndexEntry`) here.

/// The name of the library, so that the matrix has something to build and test on every architecture.
pub const NAME: &str = "atoom-common";

#[cfg(test)]
mod tests {
    use super::NAME;

    #[test]
    fn the_library_has_its_name() {
        assert_eq!(NAME, "atoom-common");
    }
}
