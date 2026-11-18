//! Connect errors with the `atoom.common.v1alpha1.Refusal` detail.

use base64::Engine as _;
use base64::engine::general_purpose::{STANDARD, STANDARD_NO_PAD};
use buffa::Message as _;
use connectrpc::ConnectError;
use connectrpc::error::{ErrorCode, ErrorDetail};

use crate::atoom::common::v1alpha1::Refusal;

/// The fully-qualified name of the detail message.
pub const DETAIL: &str = "atoom.common.v1alpha1.Refusal";

/// A Connect error with the `Refusal` detail. The message is the reason; an error never shows a value, only the path of the field at
/// fault.
#[must_use]
pub fn new(code: ErrorCode, reason: &str, field_path: &str) -> ConnectError {
    let detail = Refusal {
        reason: reason.to_owned(),
        field_path: field_path.to_owned(),
        ..Refusal::default()
    };
    ConnectError::new(code, reason).with_detail(ErrorDetail::from_message(DETAIL, &detail))
}

/// The `Refusal` detail of an error, if it has one.
#[must_use]
pub fn detail(err: &ConnectError) -> Option<Refusal> {
    err.details.iter().find_map(|d| {
        // The Connect protocol writes the bare name; a gRPC peer writes the Any type URL.
        let name = d.type_url.rsplit('/').next()?;
        if name != DETAIL {
            return None;
        }
        let raw = d.value.as_deref()?;
        let bytes = STANDARD_NO_PAD
            .decode(raw)
            .or_else(|_| STANDARD.decode(raw))
            .ok()?;
        Refusal::decode_from_slice(&bytes).ok()
    })
}

/// The reason of the `Refusal` detail of an error, if it has one.
#[must_use]
pub fn reason(err: &ConnectError) -> Option<String> {
    detail(err).map(|r| r.reason)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::atoom::common::v1alpha1::IndexEntry;

    #[test]
    fn a_refusal_carries_its_reason_and_path() {
        let e = new(ErrorCode::NotFound, "absent", "id");
        assert_eq!(e.code, ErrorCode::NotFound);
        assert_eq!(e.message.as_deref(), Some("absent"));
        assert_eq!(reason(&e).as_deref(), Some("absent"));
        assert_eq!(detail(&e).map(|r| r.field_path).as_deref(), Some("id"));
    }

    #[test]
    fn the_detail_has_the_wire_name_of_the_contract() {
        let e = new(ErrorCode::InvalidArgument, "bound", "");
        assert_eq!(e.details.len(), 1);
        assert_eq!(e.details[0].type_url, "atoom.common.v1alpha1.Refusal");
    }

    #[test]
    fn a_grpc_type_url_and_padded_base64_are_read() {
        let mut e = new(ErrorCode::Aborted, "version", "");
        let raw = e.details[0].value.clone().expect("a value");
        e.details[0].type_url = format!("type.googleapis.com/{DETAIL}");
        e.details[0].value = Some(STANDARD.encode(STANDARD_NO_PAD.decode(raw).expect("decode")));
        assert_eq!(reason(&e).as_deref(), Some("version"));
    }

    #[test]
    fn an_error_without_the_detail_has_no_reason() {
        assert_eq!(
            reason(&ConnectError::new(ErrorCode::Internal, "boom")),
            None
        );
        let mut e = new(ErrorCode::Internal, "x", "");
        e.details[0].type_url = "other.Detail".into();
        assert_eq!(reason(&e), None);
    }

    #[test]
    fn the_messages_round_trip_with_the_field_numbers_of_sluis() {
        let i = IndexEntry {
            name: "by-owner".into(),
            value: vec![1, 2, 3],
            ..IndexEntry::default()
        };
        let bytes = i.encode_to_vec();
        assert_eq!(
            bytes,
            [&[0x0a, 8][..], b"by-owner", &[0x12, 3, 1, 2, 3]].concat()
        );
        assert_eq!(IndexEntry::decode_from_slice(&bytes).expect("decode"), i);
    }
}
