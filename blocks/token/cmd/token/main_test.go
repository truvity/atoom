package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truvity/atoom/blocks/seal"
	"github.com/truvity/atoom/blocks/token"
	"github.com/truvity/atoom/blocks/token/conformance"
	"github.com/truvity/atoom/blocks/token/gen/atoom/token/v1alpha1/tokenv1alpha1connect"
	"github.com/truvity/atoom/blocks/token/internal/tokentest"
	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/purpose"
	atoomsocket "github.com/truvity/atoom/socket"
	"github.com/truvity/atoom/store/gen/atoom/store/v1alpha1/storev1alpha1connect"
)

// shortDir is a directory whose socket paths fit the 108 bytes of a UNIX socket address.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "atoom-tk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func declarationFile(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "purposes.json")
	if err := os.WriteFile(p, token.MarshalDeclaration(conformance.Purposes()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUsage(t *testing.T) {
	dir := shortDir(t)
	decl := declarationFile(t, dir)
	for _, args := range [][]string{
		{},
		{"nonsense"},
		{"serve"},
		{"serve", "--socket", dir + "/t"},
		{"serve", "--socket", dir + "/t", "--purposes", decl},
		{"serve", "--socket", dir + "/t", "--purposes", decl, "--seal-socket", "s", "--key-socket", "k"},
		{"serve", "--socket", dir + "/t", "--purposes", decl, "--key-socket", "k", "--store-socket", "s"},
		{"serve", "--socket", dir + "/t", "--purposes", dir + "/missing.json", "--seal-socket", "s"},
		{"serve", "--socket", dir + "/t", "--purposes", decl, "--seal-socket", "s", "--allow-any-uid", "--allow-uid", "1"},
		{"key-purposes"},
		{"check", "--purposes", dir + "/missing.json"},
		{"check", "--purposes", decl, "extra"},
	} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("%q: exit %d, want 2 (%s)", args, code, errb.String())
		}
	}
	var out, errb bytes.Buffer
	if code := run([]string{"--version"}, &out, &errb); code != 0 || strings.TrimSpace(out.String()) == "" {
		t.Fatalf("--version: exit %d, %q", code, out.String())
	}
	if code := run([]string{"check", "--purposes", decl}, &out, &errb); code != 0 {
		t.Fatalf("check: exit %d: %s", code, errb.String())
	}
}

func TestKeyPurposesIsWhatTheKeyDriverTakes(t *testing.T) {
	dir := shortDir(t)
	var out, errb bytes.Buffer
	if code := run([]string{"key-purposes", "--purposes", declarationFile(t, dir)}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	set, err := purpose.Parse(strings.NewReader(out.String()))
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, name := range []string{seal.DefaultWrappingPurpose, conformance.PurposeES384, conformance.PurposeES256, conformance.PurposeRS256} {
		if _, ok := set.Lookup(name); !ok {
			t.Errorf("no %s in %s", name, out.String())
		}
	}
}

// serveIn runs serve in the background until the test ends, and waits for its socket.
func serveIn(t *testing.T, args ...string) tokenv1alpha1connect.TokenServiceClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var errb bytes.Buffer
	go func() { done <- serve(ctx, args, &errb) }()
	t.Cleanup(func() {
		cancel()
		if code := <-done; code != 0 {
			t.Errorf("serve ended with %d: %s", code, errb.String())
		}
	})
	path := args[1]
	for range 200 {
		if _, err := os.Stat(path); err == nil {
			return tokenv1alpha1connect.NewTokenServiceClient(atoomsocket.Client(path), "http://token", call.ClientOptions()...)
		}
		select {
		case code := <-done:
			done <- code
			t.Fatalf("serve ended with %d before it listened: %s", code, errb.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("serve did not listen in 10 seconds")
	return nil
}

func keysAndStore(t *testing.T, dir string) (string, string) {
	t.Helper()
	sps, _ := token.SealPurposes(conformance.Purposes())
	keys := tokentest.NewKeys(seal.KeyPurposes("", sps))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	k, s := filepath.Join(dir, "k"), filepath.Join(dir, "s")
	kp, kh := keyv1alpha1connect.NewKeyStoreServiceHandler(keys)
	sp, sh := storev1alpha1connect.NewItemServiceHandler(tokentest.NoStore{})
	go func() { _ = atoomsocket.Serve(ctx, atoomsocket.Config{Socket: k}, kp, kh) }()
	go func() { _ = atoomsocket.Serve(ctx, atoomsocket.Config{Socket: s}, sp, sh) }()
	for _, p := range []string{k, s} {
		for i := 0; ; i++ {
			if _, err := os.Stat(p); err == nil {
				break
			}
			if i == 200 {
				t.Fatal("a stand-in did not listen")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	return k, s
}

func TestServeHostsASealBlockAndPassesTheKit(t *testing.T) {
	dir := shortDir(t)
	k, s := keysAndStore(t, dir)
	client := serveIn(t, "--socket", filepath.Join(dir, "t"), "--purposes", declarationFile(t, dir), "--key-socket", k, "--store-socket", s, "--installation", "thin-main-test")
	var out bytes.Buffer
	if err := conformance.RunCases(context.Background(), client, conformance.CasesWithoutRules(), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

func TestServeOverASealBlockOnASocket(t *testing.T) {
	dir := shortDir(t)
	k, s := keysAndStore(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sps, _ := token.SealPurposes(conformance.Purposes())
	sb, err := seal.New(ctx, seal.Options{
		Keys:     keyv1alpha1connect.NewKeyStoreServiceClient(atoomsocket.Client(k), "http://key", call.ClientOptions()...),
		Items:    storev1alpha1connect.NewItemServiceClient(atoomsocket.Client(s), "http://store", call.ClientOptions()...),
		Purposes: sps, Rules: seal.AllowAll(), Installation: "thin-main-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	sealSock := filepath.Join(dir, "seal")
	path, h := sb.Handler()
	go func() { _ = atoomsocket.Serve(ctx, atoomsocket.Config{Socket: sealSock}, path, h) }()
	for i := 0; ; i++ {
		if _, err := os.Stat(sealSock); err == nil {
			break
		}
		if i == 200 {
			t.Fatal("the seal block did not listen")
		}
		time.Sleep(50 * time.Millisecond)
	}
	client := serveIn(t, "--socket", filepath.Join(dir, "t"), "--purposes", declarationFile(t, dir), "--seal-socket", sealSock)
	var out bytes.Buffer
	if err := conformance.RunCases(context.Background(), client, conformance.CasesWithoutRules(), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

func TestServeFailsOverASealBlockThatDoesNotAnswer(t *testing.T) {
	dir := shortDir(t)
	var errb bytes.Buffer
	code := serve(context.Background(), []string{"--socket", filepath.Join(dir, "t"), "--purposes", declarationFile(t, dir), "--seal-socket", filepath.Join(dir, "none")}, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errb.String())
	}
}
