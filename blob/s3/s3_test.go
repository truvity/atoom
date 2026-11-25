package s3_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	blobv1alpha1 "github.com/truvity/atoom/blob/gen/atoom/blob/v1alpha1"
	"github.com/truvity/atoom/blob/gen/atoom/blob/v1alpha1/blobv1alpha1connect"
	"github.com/truvity/atoom/blob/s3"
	"github.com/truvity/atoom/blob/s3/internal/s3fake"
	"github.com/truvity/atoom/common/refusal"
)

const bucket = "test-bucket"

// client is the real AWS SDK client against url, path style, with plain bodies.
func client(url string) *awss3.Client {
	return awss3.New(awss3.Options{
		Region:                     "us-east-1",
		BaseEndpoint:               aws.String(url),
		UsePathStyle:               true,
		Credentials:                aws.AnonymousCredentials{},
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		RetryMaxAttempts:           1,
	})
}

type rig struct {
	fake *s3fake.Fake
	svc  *s3.Service
	c    blobv1alpha1connect.BlobServiceClient
}

func start(t *testing.T, opts s3.Options) *rig {
	t.Helper()
	fake := s3fake.New(bucket)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	opts.S3 = client(srv.URL)
	if opts.Bucket == "" {
		opts.Bucket = bucket
	}
	svc, err := s3.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
	api := httptest.NewServer(mux)
	t.Cleanup(api.Close)
	return &rig{fake: fake, svc: svc, c: blobv1alpha1connect.NewBlobServiceClient(api.Client(), api.URL)}
}

func reasonOf(t *testing.T, err error, wantCode connect.Code, wantReason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error; want %v/%s", wantCode, wantReason)
	}
	if got := connect.CodeOf(err); got != wantCode {
		t.Fatalf("code %v (%v); want %v", got, err, wantCode)
	}
	if got, _ := refusal.Reason(err); got != wantReason {
		t.Fatalf("reason %q; want %q", got, wantReason)
	}
}

func (r *rig) create(path string, data []byte) (string, error) {
	res, err := r.c.Create(context.Background(), connect.NewRequest(&blobv1alpha1.CreateRequest{Path: path, Data: data}))
	if err != nil {
		return "", err
	}
	return res.Msg.GetEtag(), nil
}

func (r *rig) put(path string, data []byte, ifMatch string) (string, error) {
	res, err := r.c.Put(context.Background(), connect.NewRequest(&blobv1alpha1.PutRequest{Path: path, Data: data, IfMatch: ifMatch}))
	if err != nil {
		return "", err
	}
	return res.Msg.GetEtag(), nil
}

func TestCreateIsWriteOnceAndTheTagIsTheSHA384(t *testing.T) {
	r := start(t, s3.Options{Prefix: "logs/"})
	tag, err := r.create("a/tile/0", []byte("one"))
	if err != nil || tag != s3.Tag([]byte("one")) || len(tag) != 96 {
		t.Fatalf("create = %q, %v", tag, err)
	}
	_, err = r.create("a/tile/0", []byte("two"))
	reasonOf(t, err, connect.CodeAlreadyExists, "taken")
	if d, ok := r.fake.Object(bucket, "logs/a/tile/0"); !ok || string(d) != "one" {
		t.Fatalf("the object under the prefix is %q, %v", d, ok)
	}
	got, err := r.c.Get(context.Background(), connect.NewRequest(&blobv1alpha1.BlobGetRequest{Path: "a/tile/0"}))
	if err != nil || string(got.Msg.GetData()) != "one" || got.Msg.GetEtag() != tag {
		t.Fatalf("get = %v, %v", got, err)
	}
	_, err = r.c.Get(context.Background(), connect.NewRequest(&blobv1alpha1.BlobGetRequest{Path: "a/none"}))
	reasonOf(t, err, connect.CodeNotFound, "absent")
}

// Of many creators of one path exactly one wins: S3's If-None-Match: * is the fence.
func TestOneOfManyCreatorsWins(t *testing.T) {
	r := start(t, s3.Options{})
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		wins  int
		taken int
	)
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.create("race", fmt.Appendf(nil, "%d", i))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case connect.CodeOf(err) == connect.CodeAlreadyExists:
				taken++
			default:
				t.Errorf("create: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 || taken != 31 {
		t.Fatalf("%d wins, %d taken", wins, taken)
	}
}

func TestPutOverwritesOnlyADeclaredPointerUnderItsTag(t *testing.T) {
	r := start(t, s3.Options{Pointers: []string{"head", "state"}})
	if !slices.Equal(r.svc.Pointers(), []string{"head", "state"}) {
		t.Fatalf("pointers %v", r.svc.Pointers())
	}
	if _, err := r.create("log/tile", []byte("x")); err != nil {
		t.Fatal(err)
	}
	_, err := r.put("log/tile", []byte("y"), "")
	reasonOf(t, err, connect.CodePermissionDenied, "rule")
	_, err = r.put("log/checkpoint", []byte("y"), "")
	reasonOf(t, err, connect.CodePermissionDenied, "rule")

	_, err = r.put("log/head", []byte("c1"), "stale")
	reasonOf(t, err, connect.CodeAborted, "version")
	t1, err := r.put("log/head", []byte("c1"), "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.put("log/head", []byte("c1"), "")
	reasonOf(t, err, connect.CodeAborted, "version")
	t2, err := r.put("log/head", []byte("c2"), t1)
	if err != nil || t2 == t1 {
		t.Fatalf("put under the tag = %q, %v", t2, err)
	}
	_, err = r.put("log/head", []byte("c3"), t1)
	reasonOf(t, err, connect.CodeAborted, "version")
	if d, _ := r.fake.Object(bucket, "log/head"); string(d) != "c2" {
		t.Fatalf("the pointer holds %q", d)
	}
	// A pointer under another prefix is a pointer too.
	if _, err := r.put("other/deeper/state", []byte("s"), ""); err != nil {
		t.Fatal(err)
	}
}

// A write between the driver's read and its replace makes the replace fail: the If-Match on the ETag it read is the compare-and-swap.
func TestAWriteBetweenTheReadAndTheReplaceIsAVersionRefusal(t *testing.T) {
	r := start(t, s3.Options{Pointers: []string{"head"}})
	t1, err := r.put("log/head", []byte("c1"), "")
	if err != nil {
		t.Fatal(err)
	}
	once := sync.Once{}
	r.fake.BeforePut = func(b, k string) {
		once.Do(func() { r.fake.Set(b, k, []byte("intruder")) })
	}
	_, err = r.put("log/head", []byte("c2"), t1)
	reasonOf(t, err, connect.CodeAborted, "version")
	if d, _ := r.fake.Object(bucket, "log/head"); string(d) != "intruder" {
		t.Fatalf("the pointer holds %q", d)
	}
}

// With no pointer declared, every Put is refused.
func TestNoPointerDeclaredRefusesEveryPut(t *testing.T) {
	r := start(t, s3.Options{})
	_, err := r.put("log/checkpoint", []byte("x"), "")
	reasonOf(t, err, connect.CodePermissionDenied, "rule")
	if r.fake.Puts() != 0 {
		t.Fatal("a refused put reached S3")
	}
}

func TestMalformedPathsAreRefusedBeforeS3(t *testing.T) {
	r := start(t, s3.Options{Pointers: []string{"head"}})
	for _, p := range []string{"", "../x", "/x", "a//b", "a/../b", "a/.hidden", "a\x00b", string(make([]byte, 1025))} {
		_, err := r.create(p, []byte("x"))
		reasonOf(t, err, connect.CodeInvalidArgument, "path")
		_, err = r.c.Get(context.Background(), connect.NewRequest(&blobv1alpha1.BlobGetRequest{Path: p}))
		reasonOf(t, err, connect.CodeInvalidArgument, "path")
	}
	_, err := r.c.List(context.Background(), connect.NewRequest(&blobv1alpha1.BlobListRequest{Prefix: "../"}))
	reasonOf(t, err, connect.CodeInvalidArgument, "path")
	if r.fake.Puts() != 0 {
		t.Fatal("a refused create reached S3")
	}
}

func TestListPagesInByteOrderUnderThePrefix(t *testing.T) {
	r := start(t, s3.Options{Prefix: "p/q/"})
	r.fake.Set(bucket, "outside", []byte("x"))
	r.fake.Set(bucket, "p/q/.hidden", []byte("x")) // not an object of the contract
	want := []string{"d/a", "d/b", "d/c", "d/c0", "d/e", "x"}
	for _, p := range slices.Backward(want) {
		if _, err := r.create(p, []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	for _, size := range []int32{0, 1, 2, 3, 100} {
		var got []string
		token := ""
		for pages := 0; ; pages++ {
			res, err := r.c.List(context.Background(), connect.NewRequest(&blobv1alpha1.BlobListRequest{Prefix: "", PageToken: token, PageSize: size}))
			if err != nil {
				t.Fatal(err)
			}
			if size > 0 && len(res.Msg.GetPaths()) > int(size) {
				t.Fatalf("a page of %d for %d", len(res.Msg.GetPaths()), size)
			}
			got = append(got, res.Msg.GetPaths()...)
			token = res.Msg.GetNextPageToken()
			if token == "" || pages > 20 {
				break
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("size %d: %v", size, got)
		}
	}
	res, err := r.c.List(context.Background(), connect.NewRequest(&blobv1alpha1.BlobListRequest{Prefix: "d/c"}))
	if err != nil || !slices.Equal(res.Msg.GetPaths(), []string{"d/c", "d/c0"}) {
		t.Fatalf("a prefix inside a segment: %v, %v", res, err)
	}
}

func TestAFailureOfS3IsUnavailableWithNoText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("<Error><Code>InternalError</Code><Message>secret detail</Message></Error>"))
	}))
	defer srv.Close()
	svc, err := s3.New(s3.Options{S3: client(srv.URL), Bucket: bucket})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Get(context.Background(), connect.NewRequest(&blobv1alpha1.BlobGetRequest{Path: "a"}))
	reasonOf(t, err, connect.CodeUnavailable, "engine")
	var ce *connect.Error
	if errors.As(err, &ce) && ce.Message() != "engine" {
		t.Fatalf("message %q", ce.Message())
	}
}

func TestOptions(t *testing.T) {
	c := client("http://127.0.0.1:1")
	for name, o := range map[string]s3.Options{
		"no client":          {Bucket: bucket},
		"no bucket":          {S3: c},
		"a prefix without /": {S3: c, Bucket: bucket, Prefix: "logs"},
		"a dot prefix":       {S3: c, Bucket: bucket, Prefix: "../"},
		"a pointer with /":   {S3: c, Bucket: bucket, Pointers: []string{"a/b"}},
		"an empty pointer":   {S3: c, Bucket: bucket, Pointers: []string{""}},
		"a dot pointer":      {S3: c, Bucket: bucket, Pointers: []string{".x"}},
		"a negative bound":   {S3: c, Bucket: bucket, ObjectMax: -1},
		"too many pointers":  {S3: c, Bucket: bucket, Pointers: make([]string, s3.PointersMax+1)},
	} {
		if _, err := s3.New(o); !errors.Is(err, s3.ErrOptions) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Against a real S3-compatible server when the conformance run gives one (ATOOM_S3_ENDPOINT, a bucket that exists, path style): the
// conditional writes are the server's, not the fake's.
func TestAgainstARealServer(t *testing.T) {
	url, b := os.Getenv("ATOOM_S3_ENDPOINT"), os.Getenv("ATOOM_S3_BUCKET")
	if url == "" || b == "" {
		t.Skip("no S3-compatible server (ATOOM_S3_ENDPOINT, ATOOM_S3_BUCKET)")
	}
	c := awss3.New(awss3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(url), UsePathStyle: true,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: os.Getenv("AWS_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY")}, nil
		}),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	svc, err := s3.New(s3.Options{S3: c, Bucket: b, Prefix: fmt.Sprintf("go-test-%d/", os.Getpid()), Pointers: []string{"head"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := svc.Create(ctx, connect.NewRequest(&blobv1alpha1.CreateRequest{Path: "t", Data: []byte("1")})); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create(ctx, connect.NewRequest(&blobv1alpha1.CreateRequest{Path: "t", Data: []byte("2")}))
	reasonOf(t, err, connect.CodeAlreadyExists, "taken")
	res, err := svc.Put(ctx, connect.NewRequest(&blobv1alpha1.PutRequest{Path: "x/head", Data: []byte("c1")}))
	if err != nil {
		t.Fatal(err)
	}
	t1 := res.Msg.GetEtag()
	if _, err := svc.Put(ctx, connect.NewRequest(&blobv1alpha1.PutRequest{Path: "x/head", Data: []byte("c2"), IfMatch: t1})); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Put(ctx, connect.NewRequest(&blobv1alpha1.PutRequest{Path: "x/head", Data: []byte("c3"), IfMatch: t1}))
	reasonOf(t, err, connect.CodeAborted, "version")
}
