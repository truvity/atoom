// Package s3fake is an in-process fake of the part of S3 the blob driver calls, for its tests and its conformance run: PutObject with
// If-None-Match: * and If-Match, GetObject and ListObjectsV2, on path-style URLs (/bucket/key). Objects are kept in memory. The ETag is
// the MD5 of the bytes, quoted, as S3 answers for an object written in one part without KMS. It checks no signature.
package s3fake

import (
	"crypto/md5" //nolint:gosec // S3's own ETag of a single-part object, not a security function
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Fake is an S3 of one or more buckets in memory. The zero value is not usable: call New.
type Fake struct {
	mu      sync.Mutex
	buckets map[string]map[string][]byte
	// BeforePut, when set, runs inside a PutObject after its conditions were checked and before the write, with the lock released: a
	// test makes a second write race the first there.
	BeforePut func(bucket, key string)
	puts      int
}

// New makes a fake that holds the named buckets, empty.
func New(buckets ...string) *Fake {
	f := &Fake{buckets: map[string]map[string][]byte{}}
	for _, b := range buckets {
		f.buckets[b] = map[string][]byte{}
	}
	return f
}

// Puts is the number of PutObject calls that wrote.
func (f *Fake) Puts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts
}

// Object is the bytes at key, and whether there are any.
func (f *Fake) Object(bucket, key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.buckets[bucket][key]
	return d, ok
}

// Set writes an object with no condition (a test's fixture).
func (f *Fake) Set(bucket, key string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buckets[bucket][key] = data
}

func etag(d []byte) string {
	sum := md5.Sum(d) //nolint:gosec // see the import
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

type s3error struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(s3error{Code: code, Message: code})
}

// ServeHTTP answers the calls of the driver.
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	bucketName, rawKey, _ := strings.Cut(path, "/")
	key, err := url.PathUnescape(rawKey)
	if err != nil {
		fail(w, http.StatusBadRequest, "InvalidURI")
		return
	}
	f.mu.Lock()
	_, ok := f.buckets[bucketName]
	f.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, "NoSuchBucket")
		return
	}
	switch {
	case r.Method == http.MethodPut && key != "":
		f.put(w, r, bucketName, key)
	case r.Method == http.MethodGet && key != "":
		f.get(w, bucketName, key)
	case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
		f.list(w, r, bucketName)
	default:
		fail(w, http.StatusNotImplemented, "NotImplemented")
	}
}

func (f *Fake) check(bucket, key, ifNoneMatch, ifMatch string) (int, string) {
	old, exists := f.buckets[bucket][key]
	switch {
	case ifNoneMatch == "*" && exists:
		return http.StatusPreconditionFailed, "PreconditionFailed"
	case ifNoneMatch != "" && ifNoneMatch != "*":
		return http.StatusNotImplemented, "NotImplemented"
	case ifMatch != "" && !exists:
		return http.StatusNotFound, "NoSuchKey"
	case ifMatch != "" && etag(old) != ifMatch:
		return http.StatusPreconditionFailed, "PreconditionFailed"
	}
	return 0, ""
}

func (f *Fake) put(w http.ResponseWriter, r *http.Request, bucket, key string) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		fail(w, http.StatusBadRequest, "IncompleteBody")
		return
	}
	if r.Header.Get("Content-Encoding") == "aws-chunked" || strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
		fail(w, http.StatusNotImplemented, "NotImplemented") // the client must send the body plain (checksums when required)
		return
	}
	ifNoneMatch, ifMatch := r.Header.Get("If-None-Match"), r.Header.Get("If-Match")
	f.mu.Lock()
	status, code := f.check(bucket, key, ifNoneMatch, ifMatch)
	hook := f.BeforePut
	f.mu.Unlock()
	if status != 0 {
		fail(w, status, code)
		return
	}
	if hook != nil {
		hook(bucket, key)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// The conditions hold again at the write: S3 makes the check and the write one step.
	if status, code := f.check(bucket, key, ifNoneMatch, ifMatch); status != 0 {
		fail(w, status, code)
		return
	}
	f.buckets[bucket][key] = data
	f.puts++
	w.Header().Set("ETag", etag(data))
	w.WriteHeader(http.StatusOK)
}

func (f *Fake) get(w http.ResponseWriter, bucket, key string) {
	f.mu.Lock()
	data, ok := f.buckets[bucket][key]
	f.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, "NoSuchKey")
		return
	}
	w.Header().Set("ETag", etag(data))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

type listResult struct {
	XMLName               xml.Name  `xml:"ListBucketResult"`
	Name                  string    `xml:"Name"`
	Prefix                string    `xml:"Prefix"`
	KeyCount              int       `xml:"KeyCount"`
	MaxKeys               int       `xml:"MaxKeys"`
	IsTruncated           bool      `xml:"IsTruncated"`
	Contents              []content `xml:"Contents"`
	NextContinuationToken string    `xml:"NextContinuationToken,omitempty"`
}

type content struct {
	Key  string `xml:"Key"`
	ETag string `xml:"ETag"`
	Size int    `xml:"Size"`
}

func (f *Fake) list(w http.ResponseWriter, r *http.Request, bucket string) {
	q := r.URL.Query()
	prefix, after := q.Get("prefix"), q.Get("start-after")
	if t := q.Get("continuation-token"); t != "" {
		after = t
	}
	maxKeys := 1000
	if m := q.Get("max-keys"); m != "" {
		n, err := strconv.Atoi(m)
		if err != nil || n < 0 {
			fail(w, http.StatusBadRequest, "InvalidArgument")
			return
		}
		maxKeys = min(n, 1000)
	}
	f.mu.Lock()
	var keys []string
	for k := range f.buckets[bucket] {
		if strings.HasPrefix(k, prefix) && k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	res := listResult{Name: bucket, Prefix: prefix, MaxKeys: maxKeys}
	switch {
	case maxKeys == 0:
		res.IsTruncated = len(keys) > 0
		res.NextContinuationToken = after
		keys = nil
	case len(keys) > maxKeys:
		res.IsTruncated = true
		keys = keys[:maxKeys]
		res.NextContinuationToken = keys[len(keys)-1]
	}
	for _, k := range keys {
		d := f.buckets[bucket][k]
		res.Contents = append(res.Contents, content{Key: k, ETag: etag(d), Size: len(d)})
	}
	f.mu.Unlock()
	res.KeyCount = len(res.Contents)
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(res)
}
