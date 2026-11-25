// Package s3 is the Amazon S3 driver of the blob port of atoom, atoom.blob.v1alpha1.BlobService, in Go: the blob contract over one S3
// bucket, under a key prefix the host names. The Go module is github.com/truvity/atoom/blob/s3. A Go program links it and mounts its handler
// in its own process, so that a function or a service keeps its objects in S3 with no sidecar; the thin main (cmd/s3) serves it on a UNIX
// socket.
//
//   - Create is one PutObject with If-None-Match: *, so that S3 refuses it when the key holds an object: of any number of racing creators
//     exactly one wins, and the others are already_exists.
//   - Put overwrites only a pointer, a path whose last segment the host declared (Options.Pointers). With an empty if_match it is a
//     PutObject with If-None-Match: *; with a tag it reads the pointer, compares the tag of the bytes it read with if_match, and writes
//     with If-Match on the S3 ETag of what it read, so that a write between the read and the replace makes S3 refuse it: the compare and
//     the replace are one step, also between two processes.
//   - Get is GetObject; List is ListObjectsV2 under the prefix, in byte order.
//
// The entity tag of the port is the SHA-384 of the bytes, in hex, as for the fs drivers: the driver computes it from the bytes it writes
// or reads, and never trusts S3's own ETag for it (which is not a function of the bytes under every kind of encryption).
//
// The bucket, the prefix, the client and its credentials are the host's. The driver knows no region, account or installation.
package s3

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	blobv1alpha1 "github.com/truvity/atoom/blob/gen/atoom/blob/v1alpha1"
	"github.com/truvity/atoom/blob/gen/atoom/blob/v1alpha1/blobv1alpha1connect"
	"github.com/truvity/atoom/common/refusal"
)

const (
	// PathMax is the most bytes of a path, as for every blob driver.
	PathMax = 1024
	// SegmentMax is the most bytes of one segment of a path.
	SegmentMax = 255
	// DefaultPage is the size of a page of List when the caller asks for 0.
	DefaultPage = 1000
	// PageMax is the largest page of List; a larger page size is taken as this.
	PageMax = 10_000
	// PointersMax is the most pointer names a host may declare.
	PointersMax = 64
	// ObjectMax is the default bound of one object's bytes, for a write and a read.
	ObjectMax = 64 << 20
)

// API is the part of the S3 client the driver calls; *s3.Client is one.
type API interface {
	PutObject(ctx context.Context, in *awss3.PutObjectInput, opts ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	GetObject(ctx context.Context, in *awss3.GetObjectInput, opts ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	ListObjectsV2(ctx context.Context, in *awss3.ListObjectsV2Input, opts ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error)
}

// Options are what the driver needs.
type Options struct {
	// S3 is the S3 client. Required.
	S3 API
	// Bucket is the bucket. Required.
	Bucket string
	// Prefix is put before every path to make the key: empty, or path segments ending in "/" (such as "logs/"). The driver lists and
	// reads nothing outside it.
	Prefix string
	// Pointers are the last path segments of the objects that Put may overwrite, under any prefix. Every other object is written once.
	// None declared: every Put is refused.
	Pointers []string
	// ObjectMax bounds the bytes of one object; zero is [ObjectMax].
	ObjectMax int
}

// Host is what a program that runs the driver provides.
type Host struct {
	// Serve serves h at path on the host's server until ctx ends. For a socket it is a closure around socket.Serve.
	Serve func(ctx context.Context, path string, h http.Handler) error
	// HandlerOptions are the Connect options of the handler: the read limit, the interceptors of the host.
	HandlerOptions []connect.HandlerOption
}

// ErrOptions is an [Options] that cannot make a driver.
var ErrOptions = errors.New("s3: the options are incomplete or out of range")

// Service is the blob store over one bucket and prefix: the BlobService.
type Service struct {
	blobv1alpha1connect.UnimplementedBlobServiceHandler
	api      API
	bucket   string
	prefix   string
	pointers []string
	max      int
}

var _ blobv1alpha1connect.BlobServiceHandler = (*Service)(nil)

// New makes the service of the bucket in opts. It makes no call: the bucket is first reached by a request.
func New(opts Options) (*Service, error) {
	switch {
	case opts.S3 == nil, opts.Bucket == "":
		return nil, errors.Join(ErrOptions, errors.New("s3: Options.S3 and Options.Bucket are required"))
	case opts.Prefix != "" && (!strings.HasSuffix(opts.Prefix, "/") || !validPath(strings.TrimSuffix(opts.Prefix, "/"), false)):
		return nil, errors.Join(ErrOptions, errors.New("s3: Options.Prefix is empty or path segments ending in /"))
	case len(opts.Pointers) > PointersMax:
		return nil, errors.Join(ErrOptions, errors.New("s3: too many Options.Pointers"))
	case opts.ObjectMax < 0:
		return nil, errors.Join(ErrOptions, errors.New("s3: Options.ObjectMax is negative"))
	}
	pointers := slices.Clone(opts.Pointers)
	for _, p := range pointers {
		if strings.Contains(p, "/") || !validPath(p, false) {
			return nil, errors.Join(ErrOptions, errors.New("s3: a pointer name is not one path segment"))
		}
	}
	slices.Sort(pointers)
	pointers = slices.Compact(pointers)
	maxObj := opts.ObjectMax
	if maxObj == 0 {
		maxObj = ObjectMax
	}
	return &Service{api: opts.S3, bucket: opts.Bucket, prefix: opts.Prefix, pointers: pointers, max: maxObj}, nil
}

// Pointers are the declared pointer names, sorted.
func (s *Service) Pointers() []string { return slices.Clone(s.pointers) }

// Handler returns the Connect handler of the blob service, to mount on a server or an in-memory transport.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return blobv1alpha1connect.NewBlobServiceHandler(s, opts...)
}

// Run serves the blob port on the host's server until ctx ends.
func Run(ctx context.Context, opts Options, host Host) error {
	if host.Serve == nil {
		return errors.New("s3: the host has no Serve")
	}
	svc, err := New(opts)
	if err != nil {
		return err
	}
	path, h := svc.Handler(host.HandlerOptions...)
	return host.Serve(ctx, path, h)
}

// validPath says whether p is a path of the contract: segments joined by "/", none empty (the last may be, in a prefix), none starting
// with a dot, none holding NUL, none over SegmentMax bytes, the whole at most PathMax bytes.
func validPath(p string, prefix bool) bool {
	if len(p) > PathMax {
		return false
	}
	parts := strings.Split(p, "/")
	for i, s := range parts {
		partial := prefix && i == len(parts)-1
		if (s == "" && !partial) || strings.HasPrefix(s, ".") || len(s) > SegmentMax || strings.ContainsRune(s, 0) {
			return false
		}
	}
	return true
}

// Tag is the entity tag of bytes: the SHA-384, in hex.
func Tag(data []byte) string {
	sum := sha512.Sum384(data)
	return hex.EncodeToString(sum[:])
}

func (s *Service) isPointer(path string) bool {
	name := path[strings.LastIndexByte(path, '/')+1:]
	_, found := slices.BinarySearch(s.pointers, name)
	return found
}

// engine is the refusal of a failure of S3: the reason only, and S3's text in the operator's log.
func engine(what string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	slog.Error("S3 failed", "call", what, "err", err)
	return refusal.New(connect.CodeUnavailable, "engine", "")
}

// code is the HTTP status of an S3 error, or 0.
func code(err error) int {
	var re interface{ HTTPStatusCode() int }
	if errors.As(err, &re) {
		return re.HTTPStatusCode()
	}
	return 0
}

func errCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

func absent(err error) bool {
	var nsk *types.NoSuchKey
	return errors.As(err, &nsk) || code(err) == http.StatusNotFound || errCode(err) == "NoSuchKey"
}

// precondition says whether S3 refused a conditional write: 412 Precondition Failed, or 409 ConditionalRequestConflict (a conditional
// write that raced another on the same key).
func precondition(err error) bool {
	c := code(err)
	return c == http.StatusPreconditionFailed || (c == http.StatusConflict && errCode(err) == "ConditionalRequestConflict")
}

func (s *Service) put(ctx context.Context, key string, data []byte, ifNoneMatch, ifMatch string) error {
	in := &awss3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))),
	}
	if ifNoneMatch != "" {
		in.IfNoneMatch = aws.String(ifNoneMatch)
	}
	if ifMatch != "" {
		in.IfMatch = aws.String(ifMatch)
	}
	_, err := s.api.PutObject(ctx, in)
	return err
}

// read is the bytes of the object at key and its S3 ETag; found is false for no object.
func (s *Service) read(ctx context.Context, key string) (data []byte, etag string, found bool, err error) {
	out, err := s.api.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if absent(err) {
			return nil, "", false, nil
		}
		return nil, "", false, engine("GetObject", err)
	}
	defer func() { _ = out.Body.Close() }()
	data, err = io.ReadAll(io.LimitReader(out.Body, int64(s.max)+1))
	if err != nil {
		return nil, "", false, engine("GetObject", err)
	}
	if len(data) > s.max {
		slog.Error("S3 object over the bound", "bound", s.max)
		return nil, "", false, refusal.New(connect.CodeUnavailable, "engine", "")
	}
	return data, aws.ToString(out.ETag), true, nil
}

// Create writes an object that must not exist.
func (s *Service) Create(ctx context.Context, req *connect.Request[blobv1alpha1.CreateRequest]) (*connect.Response[blobv1alpha1.CreateResponse], error) {
	path, data := req.Msg.GetPath(), req.Msg.GetData()
	if !validPath(path, false) {
		return nil, refusal.New(connect.CodeInvalidArgument, "path", "path")
	}
	if len(data) > s.max {
		return nil, refusal.New(connect.CodeInvalidArgument, "bound", "data")
	}
	if err := s.put(ctx, s.prefix+path, data, "*", ""); err != nil {
		if precondition(err) {
			return nil, refusal.New(connect.CodeAlreadyExists, "taken", "path")
		}
		return nil, engine("PutObject", err)
	}
	return connect.NewResponse(&blobv1alpha1.CreateResponse{Etag: Tag(data)}), nil
}

// Put overwrites a pointer under a condition: the tag it must have, or none for a pointer that must be absent.
func (s *Service) Put(ctx context.Context, req *connect.Request[blobv1alpha1.PutRequest]) (*connect.Response[blobv1alpha1.PutResponse], error) {
	path, data, want := req.Msg.GetPath(), req.Msg.GetData(), req.Msg.GetIfMatch()
	if !validPath(path, false) {
		return nil, refusal.New(connect.CodeInvalidArgument, "path", "path")
	}
	if !s.isPointer(path) {
		return nil, refusal.New(connect.CodePermissionDenied, "rule", "path")
	}
	if len(data) > s.max {
		return nil, refusal.New(connect.CodeInvalidArgument, "bound", "data")
	}
	key := s.prefix + path
	version := refusal.New(connect.CodeAborted, "version", "path")
	if want == "" {
		if err := s.put(ctx, key, data, "*", ""); err != nil {
			if precondition(err) {
				return nil, version
			}
			return nil, engine("PutObject", err)
		}
		return connect.NewResponse(&blobv1alpha1.PutResponse{Etag: Tag(data)}), nil
	}
	old, etag, found, err := s.read(ctx, key)
	if err != nil {
		return nil, err
	}
	if !found || Tag(old) != want || etag == "" {
		return nil, version
	}
	if err := s.put(ctx, key, data, "", etag); err != nil {
		if precondition(err) || absent(err) {
			return nil, version
		}
		return nil, engine("PutObject", err)
	}
	return connect.NewResponse(&blobv1alpha1.PutResponse{Etag: Tag(data)}), nil
}

// Get reads an object and its entity tag.
func (s *Service) Get(ctx context.Context, req *connect.Request[blobv1alpha1.BlobGetRequest]) (*connect.Response[blobv1alpha1.BlobGetResponse], error) {
	path := req.Msg.GetPath()
	if !validPath(path, false) {
		return nil, refusal.New(connect.CodeInvalidArgument, "path", "path")
	}
	data, _, found, err := s.read(ctx, s.prefix+path)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, refusal.New(connect.CodeNotFound, "absent", "path")
	}
	return connect.NewResponse(&blobv1alpha1.BlobGetResponse{Data: data, Etag: Tag(data)}), nil
}

// List answers one page of the paths under a prefix, in byte order. The page token is the last path of the page before.
func (s *Service) List(ctx context.Context, req *connect.Request[blobv1alpha1.BlobListRequest]) (*connect.Response[blobv1alpha1.BlobListResponse], error) {
	prefix, after, size := req.Msg.GetPrefix(), req.Msg.GetPageToken(), req.Msg.GetPageSize()
	if !validPath(prefix, true) || size < 0 || len(after) > PathMax {
		return nil, refusal.New(connect.CodeInvalidArgument, "path", "prefix")
	}
	switch {
	case size == 0:
		size = DefaultPage
	case size > PageMax:
		size = PageMax
	}
	in := &awss3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(s.prefix + prefix)}
	if after != "" {
		in.StartAfter = aws.String(s.prefix + after)
	}
	var (
		paths []string
		n     int32 // len(paths)
	)
	more := false
	for {
		in.MaxKeys = aws.Int32(min(size+1-n, 1000))
		out, err := s.api.ListObjectsV2(ctx, in)
		if err != nil {
			return nil, engine("ListObjectsV2", err)
		}
		for _, o := range out.Contents {
			p := strings.TrimPrefix(aws.ToString(o.Key), s.prefix)
			if !validPath(p, false) || p <= after {
				continue // not an object of the contract, or one a lenient store answered again
			}
			if n == size {
				more = true
				break
			}
			paths = append(paths, p)
			n++
		}
		if more || !aws.ToBool(out.IsTruncated) || out.NextContinuationToken == nil {
			break
		}
		in.ContinuationToken = out.NextContinuationToken
	}
	next := ""
	if more {
		next = paths[len(paths)-1]
	}
	return connect.NewResponse(&blobv1alpha1.BlobListResponse{Paths: paths, NextPageToken: next}), nil
}
