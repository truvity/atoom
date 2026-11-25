// Package conformance is the kit of the token block (docs/token-block.md, "The kit"): the cases that the block, and any other
// implementation of TokenService, must pass, run through a client of the contract.
//
// The kit declares its own purposes (Purposes: one for each algorithm and both kid rules) and its own rules (Rules), and the block under
// test is started with them over a seal block that serves SealPurposes. Its checks of what the block answers are its own: it verifies
// every signature with the standard library and makes the RFC 7638 thumbprint itself, so it does not trust the code under test. The
// block's own tests run it over a seal block on stand-ins of the ports, and `just conformance` over the memory drivers (key/software and
// store/memory), through their sockets, both with the block in the kit's process and behind the socket of its thin main.
package conformance

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/truvity/atoom/blocks/seal"
	"github.com/truvity/atoom/blocks/token"
	"github.com/truvity/atoom/blocks/token/gen/atoom/token/v1alpha1/tokenv1alpha1connect"
	"github.com/truvity/atoom/key/purpose"
)

// Client is a client of the block under test.
type Client = tokenv1alpha1connect.TokenServiceClient

// The purposes of the kit, and their types.
const (
	PurposeES384 = "kit-token-es384" // kid: the key port's key_id
	PurposeES256 = "kit-token-es256" // kid: the RFC 7638 thumbprint
	PurposeRS256 = "kit-token-rs256" // kid: the RFC 7638 thumbprint
	TypeJWT      = "JWT"
	TypeAccess   = "at+jwt"
	TypeKit      = "kit+jwt"
)

// RefusedClaim is the member whose presence in the claims the kit's rules refuse.
const RefusedClaim = "kit-refused"

// Purposes is the kit's declaration, which the block under test must serve: a purpose for each algorithm of the key port, and both kid
// rules.
func Purposes() []token.Purpose {
	return []token.Purpose{
		{Name: PurposeES384, Algorithm: purpose.ES384, Types: []string{TypeJWT, TypeAccess}, Kid: token.KidKeyID},
		{Name: PurposeES256, Algorithm: purpose.ES256, Types: []string{TypeKit}, Kid: token.KidThumbprint},
		{Name: PurposeRS256, Algorithm: purpose.RS256, Types: []string{TypeJWT}, Kid: token.KidThumbprint},
	}
}

// SealPurposes is what the seal block under the block must serve: a signing purpose for each of the kit's.
func SealPurposes() []seal.Purpose {
	ps, err := token.SealPurposes(Purposes())
	if err != nil {
		panic(err) // the kit's purposes all name an algorithm
	}
	return ps
}

// errRefused is the refusal of the kit's rules.
var errRefused = errors.New("conformance: the kit's rules refuse these claims")

// Rules are the kit's rules: every call is allowed, except a Sign whose claims name the member RefusedClaim.
func Rules() token.Rules {
	return token.RulesFunc(func(_ context.Context, r token.Request) error {
		if r.Op == token.OpSign && bytes.Contains(r.Claims, []byte(`"`+RefusedClaim+`"`)) {
			return errRefused
		}
		return nil
	})
}

// Case is one case of the kit.
type Case struct {
	Name string
	Run  func(ctx context.Context, k *Kit) error
}

// Kit is one run: the client and the run's id, which every token it signs carries.
type Kit struct {
	Client Client
	run    string
}

// Run runs every case against the block behind client, prints one line for each and a summary to out, and fails when a case failed or
// none ran.
func Run(ctx context.Context, client Client, out io.Writer) error {
	return RunCases(ctx, client, Cases(), out)
}

// RunCases runs the given cases.
func RunCases(ctx context.Context, client Client, cases []Case, out io.Writer) error {
	if len(cases) == 0 {
		return errors.New("conformance: no case ran")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	k := &Kit{Client: client, run: hex.EncodeToString(b)}
	failed := 0
	for _, c := range cases {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err := c.Run(cctx, k)
		cancel()
		if err != nil {
			failed++
			_, _ = fmt.Fprintf(out, "FAIL %s: %v\n", c.Name, err)
			continue
		}
		_, _ = fmt.Fprintf(out, "ok   %s\n", c.Name)
	}
	_, _ = fmt.Fprintf(out, "%d cases, %d ok, %d failed\n", len(cases), len(cases)-failed, failed)
	if failed > 0 {
		return fmt.Errorf("conformance: %d of %d cases failed", failed, len(cases))
	}
	return nil
}
