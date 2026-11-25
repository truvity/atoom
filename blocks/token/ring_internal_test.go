package token

import (
	"fmt"
	"testing"
	"time"

	tokenv1alpha1 "github.com/truvity/atoom/blocks/token/gen/atoom/token/v1alpha1"
	"github.com/truvity/atoom/key/purpose"
)

func TestTheTombstonesAreBounded(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	st := &tokenv1alpha1.RingState{}
	for i := range MaxDropped + 2 {
		// The ring lists keys where they joined; the drops are in another order: the second key was dropped first.
		since := now.Add(time.Duration(i) * time.Minute)
		if i == 1 {
			since = now.Add(-time.Hour)
		}
		st.Keys = append(st.Keys, &tokenv1alpha1.RingKey{Kid: fmt.Sprintf("k%d", i), State: stateDropped, AddedUnixMs: ms(now.Add(-48 * time.Hour)), StateSinceUnixMs: ms(since)})
	}
	p := Purpose{Name: "r", Algorithm: purpose.ES256, Types: []string{"JWT"}, Ring: &Ring{Lead: time.Minute, Retain: time.Minute, CacheMaxAge: time.Minute, TokenLifetime: time.Minute}}
	changes := plan(st, p, "", nil, now.Add(time.Hour*10))
	if len(st.GetKeys()) != MaxDropped || len(changes) != 2 {
		t.Fatalf("%d keys, %d changes", len(st.GetKeys()), len(changes))
	}
	for _, c := range changes {
		if c.what != ChangeForgotten || (c.kid != "k0" && c.kid != "k1") {
			t.Fatalf("forgot %+v, want the two oldest drops", c)
		}
	}
}
