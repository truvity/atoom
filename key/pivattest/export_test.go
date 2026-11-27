package pivattest

import (
	"slices"
	"time"
)

// The seams of the package, for the tests of the package pivattest_test: the embedded files, the construction over given files and a given
// table (at a fixed time, so that no test reads the clock), the cache behind Default, and the subjects of the anchors of a verifier.

// EmbeddedFiles are the root file and the bundle that Default is built from.
func EmbeddedFiles() (root, bundle []byte) { return slices.Clone(rootPEM), slices.Clone(bundlePEM) }

// BuildFrom is Default over the given files and table, at the year 2030 (inside the validity of every pinned certificate).
func BuildFrom(root, bundle []byte, table []Pin) (*Verifier, error) {
	return buildFrom(root, bundle, table, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
}

// BuildFromAt is BuildFrom at a time of the caller's.
func BuildFromAt(root, bundle []byte, table []Pin, now time.Time) (*Verifier, error) {
	return buildFrom(root, bundle, table, now)
}

// DefaultCache is the cache behind Default.
type DefaultCache = defaultCache

// Get is the build-or-keep of Default, with the clock given.
func (c *DefaultCache) Get(now func() time.Time) (*Verifier, error) { return c.get(now) }

// Kept is the verifier the cache keeps, or nil.
func (c *DefaultCache) Kept() *Verifier {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.v
}

// AnchorSubjects are the raw subjects of the anchors of a verifier.
func AnchorSubjects(v *Verifier) [][]byte {
	return v.anchors.Subjects() //nolint:staticcheck // a pool built by this package, not the system's
}
