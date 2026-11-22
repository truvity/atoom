// Package conformance is the kit of the seal block (docs/seal-block.md, "The kit"): the cases that the block, and any other
// implementation of SealService, must pass, run through a client of the contract.
//
// The kit declares its own purposes (Purposes), its own rules (Rules) and its seal limit (SealsPerGeneration, SealReservation), and the
// block under test is started with them: a host that runs the kit gives them to seal.New, and its key driver the declaration of seal.KeyPurposes(Purposes()). Every case writes under scopes
// of its own run, so the kit can run again against the same store. The block's own tests run it over in-memory stand-ins of the ports, and
// `just conformance` runs it over the memory drivers (key/software and store/memory), through their sockets.
package conformance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/truvity/atoom/blocks/seal"
	"github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1/sealv1alpha1connect"
	"github.com/truvity/atoom/key/purpose"
)

// Client is a client of the block under test.
type Client = sealv1alpha1connect.SealServiceClient

// The purposes of the kit.
const (
	PurposeSeal  = "kit-seal"
	PurposeOther = "kit-seal-other"
	PurposeMac   = "kit-mac"
	PurposeEpoch = "kit-epoch"
	PurposeES384 = "kit-es384"
	PurposeES256 = "kit-es256"
	PurposeRS256 = "kit-rs256"
	codeSeal     = 0xf1
	codeOther    = 0xf2
)

// The scopes the kit's rules treat apart, by prefix.
const (
	// RefusedPrefix starts a scope whose every call the kit's rules refuse.
	RefusedPrefix = "kit-refused-"
	// NoCreatePrefix starts a scope in which the kit's rules allow the calls and do not allow a key to be made.
	NoCreatePrefix = "kit-nocreate-"
)

// The seal limit of the block under test: the kit seals past it, so the block is started with a small one (seal.Options
// SealsPerGeneration and SealReservation).
const (
	SealsPerGeneration = 64
	SealReservation    = 8
)

// Purposes is the kit's declaration, which the block under test must serve: two seal purposes, a mac purpose of the current generation
// and one whose caller names it, and a signing purpose for each algorithm of the key port.
func Purposes() []seal.Purpose {
	return []seal.Purpose{
		{Name: PurposeSeal, Use: seal.UseSeal, Code: codeSeal},
		{Name: PurposeOther, Use: seal.UseSeal, Code: codeOther},
		{Name: PurposeMac, Use: seal.UseMac},
		{Name: PurposeEpoch, Use: seal.UseMac, CallerGeneration: true},
		{Name: PurposeES384, Use: seal.UseSign, Algorithm: purpose.ES384},
		{Name: PurposeES256, Use: seal.UseSign, Algorithm: purpose.ES256},
		{Name: PurposeRS256, Use: seal.UseSign, Algorithm: purpose.RS256},
	}
}

// errRefused is the refusal of the kit's rules.
var errRefused = errors.New("conformance: the kit's rules refuse this scope")

// Rules are the kit's rules: every call is allowed and may make a key, except in a scope that starts with RefusedPrefix (refused) or
// NoCreatePrefix (allowed, and no key is made).
func Rules() seal.Rules {
	return seal.RulesFunc(func(_ context.Context, r seal.Request) (seal.Decision, error) {
		switch {
		case strings.HasPrefix(r.Scope, RefusedPrefix):
			return seal.Decision{}, errRefused
		case strings.HasPrefix(r.Scope, NoCreatePrefix):
			return seal.Decision{}, nil
		}
		return seal.Decision{MayCreate: true}, nil
	})
}

// Case is one case of the kit.
type Case struct {
	Name string
	Run  func(ctx context.Context, k *Kit) error
}

// Kit is one run: the client and the scopes of the run.
type Kit struct {
	Client Client
	run    string
}

// Scope is a scope of this run for a case, so that a second run against the same store starts clean.
func (k *Kit) Scope(name string) string { return "kit-" + k.run + "-" + name }

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
