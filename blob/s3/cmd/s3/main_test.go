package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	blobv1alpha1 "github.com/truvity/atoom/blob/gen/atoom/blob/v1alpha1"
	"github.com/truvity/atoom/blob/gen/atoom/blob/v1alpha1/blobv1alpha1connect"
	"github.com/truvity/atoom/blob/s3"
	"github.com/truvity/atoom/blob/s3/internal/s3fake"
	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/refusal"
	atoomsocket "github.com/truvity/atoom/socket"
)

// useFake points the main at an in-process fake S3 for the duration of the test.
func useFake(t *testing.T) *s3fake.Fake {
	t.Helper()
	f := s3fake.New("b")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	old := newClient
	t.Cleanup(func() { newClient = old })
	newClient = func(context.Context, bool) (s3.API, error) {
		return awss3.New(awss3.Options{
			Region: "us-east-1", BaseEndpoint: aws.String(srv.URL), UsePathStyle: true, Credentials: aws.AnonymousCredentials{},
			RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
			ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		}), nil
	}
	return f
}

func TestUsage(t *testing.T) {
	base := []string{"serve", "--socket", "/x", "--bucket", "b"}
	with := func(extra ...string) []string { return append(append([]string(nil), base...), extra...) }
	for _, args := range [][]string{
		nil, {"nope"}, {"serve"}, {"serve", "--socket", "/x"}, {"serve", "--bucket", "b"}, with("extra"),
		with("--socket-mode", "zz"), with("--pointer", "a/b"), with("--pointer", ".x"), with("--prefix", "nos"),
	} {
		useFake(t)
		var errb bytes.Buffer
		if code := run(args, &bytes.Buffer{}, &errb); code != 2 {
			t.Errorf("run(%v) = %d, want 2 (%s)", args, code, errb.String())
		}
	}
}

func TestVersion(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		var out bytes.Buffer
		if code := run([]string{arg}, &out, &bytes.Buffer{}); code != 0 || strings.TrimSpace(out.String()) == "" {
			t.Fatalf("%s: %d %q", arg, code, out.String())
		}
	}
}

func TestTheMainServesThroughItsSocket(t *testing.T) {
	f := useFake(t)
	dir, err := os.MkdirTemp("", "s3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"serve", "--socket", socket, "--bucket", "b", "--prefix", "p/", "--pointer", "head", "--path-style"},
			&bytes.Buffer{}, &bytes.Buffer{})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if fi, err := os.Lstat(socket); err == nil && fi.Mode()&os.ModeSocket != 0 {
			break
		}
		select {
		case code := <-done:
			t.Fatalf("serve returned early: %d", code)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the socket did not appear")
		}
		time.Sleep(5 * time.Millisecond)
	}
	c := blobv1alpha1connect.NewBlobServiceClient(atoomsocket.Client(socket), "http://blob", call.ClientOptions()...)
	ctx := context.Background()
	_, cerr := c.Create(ctx, connect.NewRequest(&blobv1alpha1.CreateRequest{Path: "log/tile", Data: []byte("x")}))
	_, perr := c.Put(ctx, connect.NewRequest(&blobv1alpha1.PutRequest{Path: "log/head", Data: []byte("h")}))
	_, rerr := c.Put(ctx, connect.NewRequest(&blobv1alpha1.PutRequest{Path: "log/tile", Data: []byte("y")}))
	p, _ := os.FindProcess(os.Getpid())
	_ = p.Signal(os.Interrupt)
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("serve ended with %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Error("serve did not end")
	}
	if cerr != nil || perr != nil {
		t.Fatalf("create %v, put %v", cerr, perr)
	}
	if reason, _ := refusal.Reason(rerr); reason != "rule" {
		t.Fatalf("a put of a path that is not a pointer: %v", rerr)
	}
	if d, ok := f.Object("b", "p/log/head"); !ok || string(d) != "h" {
		t.Fatalf("the pointer under the prefix: %q %v", d, ok)
	}
}
