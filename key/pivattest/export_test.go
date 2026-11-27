package pivattest

import "slices"

// The seams of the package, for the tests of the package pivattest_test: the embedded files, the construction over given files and a given
// table, and the subjects of the anchors of a verifier.

// EmbeddedFiles are the root file and the bundle that Default is built from.
func EmbeddedFiles() (root, bundle []byte) { return slices.Clone(rootPEM), slices.Clone(bundlePEM) }

// BuildFrom is Default over the given files and table.
func BuildFrom(root, bundle []byte, table []Pin) (*Verifier, error) {
	return buildFrom(root, bundle, table)
}

// AnchorSubjects are the raw subjects of the anchors of a verifier.
func AnchorSubjects(v *Verifier) [][]byte {
	return v.anchors.Subjects() //nolint:staticcheck // a pool built by this package, not the system's
}
