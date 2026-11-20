//! The client of the destination port: the [`Destination`] that a consumer calls a driver's socket with.
//!
//! [`Client`] maps each of the five calls to the contract and each refusal back to an [`Error`]: `unimplemented` is
//! [`Error::Unsupported`], `invalid_argument` is [`Error::Invalid`], anything else is [`Error::Unavailable`] with the code and the reason of
//! the `Refusal` detail. A document is checked with [`check_document`] before it is sent, so one the wire would not take is refused here
//! with its field.
//!
//! The client is generic over the transport of the generated client, so this crate needs no socket host: a consumer builds it over the
//! transport of `atoom_socket::dial` (`Client::new(dial.transport(), dial.config())`), which sends the deadline and the correlation id of
//! the call being served on every call (`docs/call-context.md`).

use atoom_common::refusal::reason as refusal_reason;
use connectrpc::ConnectError;
use connectrpc::client::{ClientConfig, ClientTransport};
use connectrpc::error::ErrorCode;
use connectrpc::http_body::Body;

use crate::atoom::destination::v1alpha1::{
    DeleteRequest, DestinationServiceClient, ListRequest, PurgeRequest, PurgeResult, PutRequest,
    ReadBackRequest,
};
use crate::{Destination, Document, Error, Fetched, Listed, Page, Purged, Written, check_document};

/// The [`Destination`] behind a driver's socket (or any other transport of a generated client).
#[derive(Clone)]
pub struct Client<T> {
    inner: DestinationServiceClient<T>,
}

impl<T> Client<T>
where
    T: ClientTransport,
    <T::ResponseBody as Body>::Error: std::fmt::Display,
{
    /// A client over `transport`, with the configuration `config` (for the socket client of `atoom-socket`, `dial.transport()` and
    /// `dial.config()`). The connection, if the transport makes one, is made at the first call.
    #[must_use]
    pub fn new(transport: T, config: ClientConfig) -> Self {
        Self {
            inner: DestinationServiceClient::new(transport, config),
        }
    }
}

/// The [`Error`] of a refusal of the driver.
#[must_use]
pub fn error_of(e: &ConnectError) -> Error {
    match e.code {
        ErrorCode::Unimplemented => Error::Unsupported,
        ErrorCode::InvalidArgument => Error::Invalid("remote"),
        _ => Error::Unavailable(format!(
            "{} ({})",
            e.code.as_str(),
            refusal_reason(e).unwrap_or_else(|| "-".into())
        )),
    }
}

impl<T> Destination for Client<T>
where
    T: ClientTransport + Send + Sync,
    <T::ResponseBody as Body>::Error: std::fmt::Display,
{
    async fn put(&self, doc: Document) -> Result<Written, Error> {
        // The same rule as the server's, so a document the wire would not take is refused here with its field.
        check_document(&doc)?;
        let r = self
            .inner
            .put(PutRequest {
                address: doc.address,
                content_type: doc.content_type,
                cache_control: doc.cache_control,
                body: doc.body,
                ..PutRequest::default()
            })
            .await
            .map_err(|e| error_of(&e))?
            .into_owned();
        Ok(Written {
            version: r.version,
            sha256: r.sha256,
        })
    }

    async fn delete(&self, address: &str) -> Result<bool, Error> {
        let r = self
            .inner
            .delete(DeleteRequest {
                address: address.to_owned(),
                ..DeleteRequest::default()
            })
            .await
            .map_err(|e| error_of(&e))?
            .into_owned();
        Ok(r.removed)
    }

    async fn list(&self, prefix: &str, page: Option<&str>, size: usize) -> Result<Page, Error> {
        let r = self
            .inner
            .list(ListRequest {
                prefix: prefix.to_owned(),
                page_token: page.unwrap_or_default().to_owned(),
                page_size: i32::try_from(size).unwrap_or(i32::MAX),
                ..ListRequest::default()
            })
            .await
            .map_err(|e| error_of(&e))?
            .into_owned();
        Ok(Page {
            documents: r
                .documents
                .into_iter()
                .map(|d| Listed {
                    address: d.address,
                    sha256: d.sha256,
                    content_type: d.content_type,
                    cache_control: d.cache_control,
                    size: d.size,
                })
                .collect(),
            next: (!r.next_page_token.is_empty()).then_some(r.next_page_token),
        })
    }

    async fn purge(&self, addresses: &[String]) -> Result<Purged, Error> {
        let r = self
            .inner
            .purge(PurgeRequest {
                addresses: addresses.to_vec(),
                ..PurgeRequest::default()
            })
            .await
            .map_err(|e| error_of(&e))?
            .into_owned();
        match r.result.as_known() {
            Some(PurgeResult::PURGE_RESULT_NONE) => Ok(Purged::None),
            Some(PurgeResult::PURGE_RESULT_PURGED) => Ok(Purged::Purged),
            _ => Err(Error::Unavailable("purge: an answer with no result".into())),
        }
    }

    async fn read_back(&self, address: &str) -> Result<Fetched, Error> {
        let r = self
            .inner
            .read_back(ReadBackRequest {
                address: address.to_owned(),
                ..ReadBackRequest::default()
            })
            .await
            .map_err(|e| error_of(&e))?
            .into_owned();
        Ok(Fetched {
            status: u16::try_from(r.status).unwrap_or(0),
            cache_control: r.cache_control,
            content_type: r.content_type,
            age: r.age,
            cache_state: r.cache_state,
            body: r.body,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use atoom_common::refusal;

    #[test]
    fn a_refusal_maps_to_the_error_of_the_port() {
        assert_eq!(
            error_of(&refusal::new(ErrorCode::Unimplemented, "unsupported", "")),
            Error::Unsupported
        );
        assert_eq!(
            error_of(&refusal::new(
                ErrorCode::InvalidArgument,
                "invalid",
                "address"
            )),
            Error::Invalid("remote")
        );
        assert_eq!(
            error_of(&refusal::new(ErrorCode::Unavailable, "engine", "")),
            Error::Unavailable("unavailable (engine)".into())
        );
        assert_eq!(
            error_of(&ConnectError::new(ErrorCode::Internal, "x")),
            Error::Unavailable("internal (-)".into())
        );
    }
}
