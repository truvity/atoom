package conformance_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/key/conformance"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/purpose"
)

// capsOnly is a driver that answers Capabilities and nothing else: enough to test what the kit skips and what it fails.
type capsOnly struct {
	keyv1alpha1connect.UnimplementedKeyStoreServiceHandler
	algorithms []string
	purposes   []*keyv1alpha1.PurposeInfo
	attests    bool
	err        error
}

func (c *capsOnly) Capabilities(context.Context, *connect.Request[keyv1alpha1.CapabilitiesRequest]) (*connect.Response[keyv1alpha1.CapabilitiesResponse], error) {
	if c.err != nil {
		return nil, c.err
	}
	return connect.NewResponse(&keyv1alpha1.CapabilitiesResponse{Store: "fake", Attests: c.attests, Algorithms: c.algorithms, Purposes: c.purposes}), nil
}

func client(t *testing.T, h keyv1alpha1connect.KeyStoreServiceHandler) conformance.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(keyv1alpha1connect.NewKeyStoreServiceHandler(h))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return keyv1alpha1connect.NewKeyStoreServiceClient(srv.Client(), srv.URL)
}

func ran(string) func(context.Context, conformance.Client) error {
	return func(context.Context, conformance.Client) error { return nil }
}

func TestAnEmptySuiteFails(t *testing.T) {
	var out bytes.Buffer
	err := conformance.RunCases(context.Background(), client(t, &capsOnly{}), nil, &out)
	if err == nil || !strings.Contains(err.Error(), "no case ran") {
		t.Fatalf("an empty suite: %v", err)
	}
}

func TestASuiteOfSkippedCasesFails(t *testing.T) {
	var out bytes.Buffer
	cases := []conformance.Case{{Name: "needs RS256", Algorithm: conformance.RS256, Run: ran("")}}
	err := conformance.RunCases(context.Background(), client(t, &capsOnly{algorithms: []string{conformance.ES384}}), cases, &out)
	if err == nil || !strings.Contains(out.String(), "skip needs RS256: the store does not list RS256") {
		t.Fatalf("err=%v out=%s", err, out.String())
	}
}

func TestACaseThatNeedsWhatTheStoreLacksIsSkippedAndTheRestRun(t *testing.T) {
	var out bytes.Buffer
	cases := []conformance.Case{
		{Name: "plain", Run: ran("")},
		{Name: "needs RS256", Algorithm: conformance.RS256, Run: func(context.Context, conformance.Client) error { return errors.New("must not run") }},
		{Name: "needs attestation", Attests: true, Run: func(context.Context, conformance.Client) error { return errors.New("must not run") }},
		{Name: "needs ES384", Algorithm: conformance.ES384, Run: ran("")},
	}
	err := conformance.RunCases(context.Background(), client(t, &capsOnly{algorithms: []string{conformance.ES384}}), cases, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, want := range []string{"ok   plain", "skip needs RS256", "skip needs attestation: the store does not attest", "ok   needs ES384", "4 cases, 2 ok, 0 failed, 2 skipped"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out.String())
		}
	}
}

func TestAFailedCaseFailsTheRunAndTheRunGoesOn(t *testing.T) {
	var out bytes.Buffer
	cases := []conformance.Case{
		{Name: "bad", Run: func(context.Context, conformance.Client) error { return errors.New("no") }},
		{Name: "good", Run: ran("")},
	}
	err := conformance.RunCases(context.Background(), client(t, &capsOnly{algorithms: []string{conformance.ES384}}), cases, &out)
	if err == nil || !strings.Contains(out.String(), "FAIL bad: no") || !strings.Contains(out.String(), "ok   good") {
		t.Fatalf("err=%v out=%s", err, out.String())
	}
}

func TestADriverThatCannotSayWhatItOffersFails(t *testing.T) {
	var out bytes.Buffer
	cases := []conformance.Case{{Name: "plain", Run: ran("")}}
	err := conformance.RunCases(context.Background(), client(t, &capsOnly{err: connect.NewError(connect.CodeUnavailable, errors.New("down"))}), cases, &out)
	if err == nil || !strings.Contains(out.String(), "FAIL plain: the Capabilities call") {
		t.Fatalf("err=%v out=%s", err, out.String())
	}
}

// The capabilities case itself rejects a store that lists an algorithm that is not the contract's, or none.
func TestTheCapabilitiesCase(t *testing.T) {
	for name, tc := range map[string]struct {
		d    *capsOnly
		want string
	}{
		"no algorithm":       {&capsOnly{}, "lists no algorithm"},
		"a foreign one":      {&capsOnly{algorithms: []string{"PS256"}}, "not an algorithm of the contract"},
		"a good declaration": {&capsOnly{algorithms: []string{conformance.ES384}}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			rs := conformance.Results(context.Background(), client(t, tc.d), conformance.Suite()[:1])
			switch {
			case tc.want == "" && rs[0].Err != nil:
				t.Fatalf("refused: %v", rs[0].Err)
			case tc.want != "" && (rs[0].Err == nil || !strings.Contains(rs[0].Err.Error(), tc.want)):
				t.Fatalf("err = %v, want %q", rs[0].Err, tc.want)
			}
		})
	}
}

func TestTheSuiteHasItsCases(t *testing.T) {
	if n := len(conformance.Suite()); n != 12 {
		t.Fatalf("%d cases", n)
	}
}

// The kit's declaration is a good one, holds each algorithm of the contract, and uses no name of the closed lists that the drivers once
// held: a pass shows that a driver serves what its host declares.
func TestTheKitsDeclarationIsItsOwn(t *testing.T) {
	set, err := purpose.New(conformance.Declaration())
	if err != nil {
		t.Fatal(err)
	}
	algs := map[string]bool{}
	for _, d := range set.List() {
		algs[d.Algorithm] = true
		if _, old := purpose.MustNew(purpose.Reference()).Lookup(d.Name); old && d.Name != conformance.PurposeRelease {
			t.Errorf("%s is a name of the reference list", d.Name)
		}
	}
	for _, a := range []string{conformance.ES384, conformance.ES256, conformance.RS256, conformance.ECDHP384} {
		if !algs[a] {
			t.Errorf("no purpose of %s", a)
		}
	}
	if _, ok := set.Lookup(conformance.PurposeUndeclared); ok {
		t.Error("the undeclared purpose is declared")
	}
	// Every purpose that a case names is in the declaration.
	for _, c := range conformance.Suite() {
		for _, name := range c.Purposes {
			if _, ok := set.Lookup(name); !ok {
				t.Errorf("%s: the case names %s, which the declaration lacks", c.Name, name)
			}
		}
	}
}

// A store that lists the algorithm of a case and does not serve the kit's purposes fails the case; a store that does not list the
// algorithm skips it. The reason says what to do.
func TestACaseFailsWhenTheStoreDoesNotServeTheKitsPurposes(t *testing.T) {
	cases := []conformance.Case{{Name: "needs the es384 purpose", Algorithm: conformance.ES384, Purposes: []string{conformance.PurposeES384}, Run: ran("")}}
	run := func(d *capsOnly) string {
		var out bytes.Buffer
		_ = conformance.RunCases(context.Background(), client(t, d), append(cases, conformance.Case{Name: "plain", Run: ran("")}), &out)
		return out.String()
	}
	if got := run(&capsOnly{algorithms: []string{conformance.ES384}}); !strings.Contains(got, "FAIL needs the es384 purpose: the store does not serve the purpose conformance-es384") {
		t.Errorf("no purposes declared:\n%s", got)
	}
	other := []*keyv1alpha1.PurposeInfo{{Name: "token-signing", Algorithm: conformance.ES384, Operations: []string{"sign"}}}
	if got := run(&capsOnly{algorithms: []string{conformance.ES384}, purposes: other}); !strings.Contains(got, "FAIL needs the es384 purpose") {
		t.Errorf("another purpose declared:\n%s", got)
	}
	wrong := []*keyv1alpha1.PurposeInfo{{Name: conformance.PurposeES384, Algorithm: conformance.ES256, Operations: []string{"sign"}}}
	if got := run(&capsOnly{algorithms: []string{conformance.ES384}, purposes: wrong}); !strings.Contains(got, "another algorithm or operations") {
		t.Errorf("another algorithm declared:\n%s", got)
	}
	right := []*keyv1alpha1.PurposeInfo{{Name: conformance.PurposeES384, Algorithm: conformance.ES384, Operations: []string{"sign"}}}
	if got := run(&capsOnly{algorithms: []string{conformance.ES384}, purposes: right}); !strings.Contains(got, "ok   needs the es384 purpose") {
		t.Errorf("the right purpose declared:\n%s", got)
	}
	if got := run(&capsOnly{algorithms: []string{conformance.ES256}}); !strings.Contains(got, "skip needs the es384 purpose: the store does not list ES384") {
		t.Errorf("an algorithm the store lacks:\n%s", got)
	}
}

// The capabilities case rejects purposes that are not a declaration.
func TestTheCapabilitiesCaseChecksThePurposes(t *testing.T) {
	bad := []*keyv1alpha1.PurposeInfo{{Name: "Bad", Algorithm: conformance.ES384}}
	rs := conformance.Results(context.Background(), client(t, &capsOnly{algorithms: []string{conformance.ES384}, purposes: bad}), conformance.Suite()[:1])
	if rs[0].Err == nil || !strings.Contains(rs[0].Err.Error(), "not a declaration") {
		t.Fatalf("err = %v", rs[0].Err)
	}
}
