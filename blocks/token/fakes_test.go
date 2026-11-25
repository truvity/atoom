package token_test

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/atoom/blocks/seal"
	"github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1/sealv1alpha1connect"
	"github.com/truvity/atoom/blocks/token"
	"github.com/truvity/atoom/blocks/token/gen/atoom/token/v1alpha1/tokenv1alpha1connect"
	"github.com/truvity/atoom/blocks/token/internal/tokentest"
	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/socket"
	"github.com/truvity/atoom/store/gen/atoom/store/v1alpha1/storev1alpha1connect"
)

// stack is a seal block over fakeKeys, and a token block over it, each called in-process through its client.
type stack struct {
	keys   *tokentest.Keys
	items  *tokentest.Items
	store  storev1alpha1connect.ItemServiceClient
	seal   sealv1alpha1connect.SealServiceClient
	client tokenv1alpha1connect.TokenServiceClient
	block  *token.Block
}

type stackOptions struct {
	purposes  []token.Purpose
	rules     token.Rules
	sealRules seal.Rules
	observe   func(context.Context, token.Event)
	keys      keyv1alpha1connect.KeyStoreServiceClient // instead of one fakeKeys
	items     *tokentest.Items                         // the rings' store; a new one when nil
	node      string
	clock     func() time.Time
}

func newStack(t *testing.T, o stackOptions) *stack {
	t.Helper()
	ctx := context.Background()
	sps, err := token.SealPurposes(o.purposes)
	if err != nil {
		t.Fatal(err)
	}
	s := &stack{}
	keys := o.keys
	if keys == nil {
		s.keys = tokentest.NewKeys(seal.KeyPurposes("", sps))
		hc, stop := socket.InProcess(keyv1alpha1connect.NewKeyStoreServiceHandler(s.keys))
		t.Cleanup(stop)
		keys = keyv1alpha1connect.NewKeyStoreServiceClient(hc, "http://key", call.ClientOptions()...)
	}
	shc, sstop := socket.InProcess(storev1alpha1connect.NewItemServiceHandler(tokentest.NoStore{}))
	t.Cleanup(sstop)
	if o.sealRules == nil {
		o.sealRules = seal.AllowAll()
	}
	sb, err := seal.New(ctx, seal.Options{
		Keys: keys, Items: storev1alpha1connect.NewItemServiceClient(shc, "http://store", call.ClientOptions()...),
		Purposes: sps, Rules: o.sealRules, Installation: "token-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sb.Close)
	hc, stop := socket.InProcess(sb.Handler())
	t.Cleanup(stop)
	s.seal = sealv1alpha1connect.NewSealServiceClient(hc, "http://seal", call.ClientOptions()...)
	if o.rules == nil {
		o.rules = token.AllowAll()
	}
	s.items = o.items
	if s.items == nil {
		s.items = tokentest.NewItems()
	}
	ihc, istop := socket.InProcess(storev1alpha1connect.NewItemServiceHandler(s.items))
	t.Cleanup(istop)
	s.store = storev1alpha1connect.NewItemServiceClient(ihc, "http://items", call.ClientOptions()...)
	s.block, err = token.New(ctx, token.Options{Seal: s.seal, Purposes: o.purposes, Rules: o.rules, Observe: o.observe, Items: s.store, Node: o.node, Clock: o.clock})
	if err != nil {
		t.Fatal(err)
	}
	thc, tstop := socket.InProcess(s.block.Handler())
	t.Cleanup(tstop)
	s.client = tokenv1alpha1connect.NewTokenServiceClient(thc, "http://token", call.ClientOptions()...)
	return s
}
