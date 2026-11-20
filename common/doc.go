// Package common is the root of the common library: the types every atoom port and driver speaks.
//
// The contract is the proto package atoom.common.v1alpha1 (Refusal, IndexEntry), generated into gen/atoom/common/v1alpha1. The
// subpackage refusal makes and reads the Connect errors that carry the Refusal detail, and the subpackage call hands the deadline and
// the correlation id of a call down to the calls it makes. The Rust twin is the crate atoom-common.
package common
