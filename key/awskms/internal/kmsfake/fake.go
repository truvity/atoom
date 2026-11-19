// Package kmsfake is a fake of AWS KMS for the tests of the driver: an HTTP handler that speaks the KMS JSON protocol (awsJson1_1,
// X-Amz-Target TrentService.<Operation>), so that the driver is tested through the real AWS SDK client, its serialisation included, and
// with real cryptography behind it. It makes keys of the specs the driver uses (ECC_NIST_P384, ECC_NIST_P256, RSA_2048), signs digests,
// derives ECDH secrets, keeps aliases and tags, and can be told to fail a call.
//
// It is not a KMS emulator. It knows only the seven operations the driver calls, it checks only what the driver can get wrong, and
// where it is unsure how the real service answers (an error code, a quota) the driver's docs say so (key/awskms/docs/research).
package kmsfake

import (
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// account and region of the ARNs that the fake makes: the example account of the AWS documentation.
const (
	account = "111122223333"
	region  = "us-east-1"
)

// Serve starts an HTTP server for the fake and returns a KMS client of the real AWS SDK that talks to it, with credentials that are
// placeholders and no retries (so that a failure the fake is told to make is seen once), and the function that stops the server.
func (f *Fake) Serve() (*kms.Client, func()) {
	srv := httptest.NewServer(f)
	client := kms.New(kms.Options{
		Region:       region,
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   srv.Client(),
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "placeholder", SecretAccessKey: "placeholder"}, nil
		}),
		RetryMaxAttempts: 1,
	})
	return client, srv.Close
}

// Key is a key of the fake as a test sees it.
type Key struct {
	ID           string
	Spec         string
	Usage        string
	State        string
	Tags         map[string]string
	Created      time.Time
	DeletionDays int // the pending window of a key that was scheduled for deletion
	priv         crypto.Signer
}

// ARN is the ARN of the key.
func (k *Key) ARN() string { return fmt.Sprintf("arn:aws:kms:%s:%s:key/%s", region, account, k.ID) }

// Fake is the state of one fake KMS. It is safe for concurrent use.
type Fake struct {
	mu      sync.Mutex
	keys    map[string]*Key
	aliases map[string]string // alias name -> key ID
	calls   map[string]int
	fail    map[string]string // operation -> error code, until cleared
	lag     map[string]int    // operation -> how many more calls answer NotFoundException (eventual consistency)
	hook    func(op string)   // called with the operation name before it is served, without the lock
	now     func() time.Time
}

// New returns an empty fake.
func New() *Fake {
	return &Fake{keys: map[string]*Key{}, aliases: map[string]string{}, calls: map[string]int{}, fail: map[string]string{}, lag: map[string]int{}, now: time.Now}
}

// SetHook sets a function that runs before each operation is served (a test that holds a call to make two race).
func (f *Fake) SetHook(h func(op string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hook = h
}

// Fail makes every call of the operation answer the KMS error code (for example "ThrottlingException") until Fail is called with "".
func (f *Fake) Fail(op, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if code == "" {
		delete(f.fail, op)
		return
	}
	f.fail[op] = code
}

// Lag makes the next n calls of the operation answer NotFoundException, as KMS does for a key that was made a moment ago and is not
// visible everywhere yet (its API is eventually consistent).
func (f *Fake) Lag(op string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lag[op] = n
}

// Calls returns how many times an operation was called.
func (f *Fake) Calls(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

// KeyByAlias returns the key that an alias names, or nil.
func (f *Fake) KeyByAlias(alias string) *Key {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.aliases[alias]
	if !ok {
		return nil
	}
	k := *f.keys[id]
	return &k
}

// Keys returns a copy of every key, in no order.
func (f *Fake) Keys() []Key {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Key, 0, len(f.keys))
	for _, k := range f.keys {
		out = append(out, *k)
	}
	return out
}

// SetState sets the state of a key ("Enabled", "Disabled", "PendingDeletion"), as an operator would.
func (f *Fake) SetState(id, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[id].State = state
}

// AddKey makes a key of a spec and usage under an alias, as a deployment's code would, and returns it.
func (f *Fake) AddKey(alias, spec, usage string) (*Key, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, err := f.newKey(spec, usage)
	if err != nil {
		return nil, err
	}
	if alias != "" {
		f.aliases[alias] = k.ID
	}
	c := *k
	return &c, nil
}

type apiError struct {
	code string
	msg  string
}

func (e *apiError) Error() string { return e.code + ": " + e.msg }

func errorf(code, format string, args ...any) *apiError {
	return &apiError{code: code, msg: fmt.Sprintf(format, args...)}
}

// ServeHTTP serves one KMS call.
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "TrentService.")
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls[op]++
	code, failing := f.fail[op]
	if f.lag[op] > 0 {
		f.lag[op]--
		code, failing = "NotFoundException", true
	}
	hook := f.hook
	f.mu.Unlock()
	if hook != nil {
		hook(op)
	}
	var out any
	var err error
	switch {
	case failing:
		err = errorf(code, "the fake was told to fail %s", op)
	default:
		out, err = f.dispatch(op, body)
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	if err != nil {
		var ae *apiError
		if !errors.As(err, &ae) {
			ae = errorf("KMSInternalException", "%v", err)
		}
		w.Header().Set("X-Amzn-ErrorType", ae.code)
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"__type": ae.code, "message": ae.msg})
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (f *Fake) dispatch(op string, body []byte) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch op {
	case "CreateKey":
		return f.createKey(body)
	case "CreateAlias":
		return f.createAlias(body)
	case "DescribeKey":
		return f.describeKey(body)
	case "GetPublicKey":
		return f.getPublicKey(body)
	case "Sign":
		return f.sign(body)
	case "DeriveSharedSecret":
		return f.deriveSharedSecret(body)
	case "ScheduleKeyDeletion":
		return f.scheduleKeyDeletion(body)
	}
	return nil, errorf("UnknownOperationException", "the fake does not know %q", op)
}

type metadata struct {
	KeyID        string  `json:"KeyId"`
	Arn          string  `json:"Arn"`
	KeySpec      string  `json:"KeySpec"`
	KeyUsage     string  `json:"KeyUsage"`
	KeyState     string  `json:"KeyState"`
	Origin       string  `json:"Origin"`
	Enabled      bool    `json:"Enabled"`
	CreationDate float64 `json:"CreationDate"`
	Description  string  `json:"Description,omitempty"`
}

func (k *Key) metadata() metadata {
	return metadata{
		KeyID: k.ID, Arn: k.ARN(), KeySpec: k.Spec, KeyUsage: k.Usage, KeyState: k.State, Origin: "AWS_KMS",
		Enabled: k.State == "Enabled", CreationDate: float64(k.Created.UnixMilli()) / 1000,
	}
}

func (f *Fake) newKey(spec, usage string) (*Key, error) {
	var priv crypto.Signer
	var err error
	switch spec {
	case "ECC_NIST_P384":
		priv, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case "ECC_NIST_P256":
		priv, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "RSA_2048":
		priv, err = rsa.GenerateKey(rand.Reader, 2048)
	default:
		return nil, errorf("ValidationException", "the fake does not make %q keys", spec)
	}
	if err != nil {
		return nil, err
	}
	if usage == "KEY_AGREEMENT" && strings.HasPrefix(spec, "RSA") {
		return nil, errorf("ValidationException", "an RSA key cannot be used for key agreement")
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	h := hex.EncodeToString(id)
	idStr := h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	k := &Key{ID: idStr, Spec: spec, Usage: usage, State: "Enabled", Tags: map[string]string{}, Created: f.now().Truncate(time.Millisecond), priv: priv}
	f.keys[k.ID] = k
	return k, nil
}

// resolve finds a key by ID, ARN or alias name.
func (f *Fake) resolve(ref string) (*Key, error) {
	if strings.HasPrefix(ref, "alias/") {
		id, ok := f.aliases[ref]
		if !ok {
			return nil, errorf("NotFoundException", "alias %s is not found", ref)
		}
		return f.keys[id], nil
	}
	id := ref
	if strings.HasPrefix(ref, "arn:") {
		id = ref[strings.LastIndex(ref, "/")+1:]
	}
	k, ok := f.keys[id]
	if !ok {
		return nil, errorf("NotFoundException", "key %s is not found", ref)
	}
	return k, nil
}

func (f *Fake) usable(k *Key) error {
	switch k.State {
	case "Enabled":
		return nil
	case "PendingDeletion":
		return errorf("KMSInvalidStateException", "key is pending deletion")
	default:
		return errorf("DisabledException", "key is disabled")
	}
}

func (f *Fake) createKey(body []byte) (any, error) {
	var in struct {
		KeySpec  string
		KeyUsage string
		Tags     []struct{ TagKey, TagValue string }
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, errorf("ValidationException", "bad request: %v", err)
	}
	k, err := f.newKey(in.KeySpec, in.KeyUsage)
	if err != nil {
		return nil, err
	}
	for _, t := range in.Tags {
		k.Tags[t.TagKey] = t.TagValue
	}
	return map[string]any{"KeyMetadata": k.metadata()}, nil
}

func (f *Fake) createAlias(body []byte) (any, error) {
	var in struct {
		AliasName   string `json:"AliasName"`
		TargetKeyID string `json:"TargetKeyId"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, errorf("ValidationException", "bad request: %v", err)
	}
	if !strings.HasPrefix(in.AliasName, "alias/") || strings.HasPrefix(in.AliasName, "alias/aws/") {
		return nil, errorf("ValidationException", "alias name %q is not allowed", in.AliasName)
	}
	k, err := f.resolve(in.TargetKeyID)
	if err != nil {
		return nil, err
	}
	if _, taken := f.aliases[in.AliasName]; taken {
		return nil, errorf("AlreadyExistsException", "an alias with the name %s already exists", in.AliasName)
	}
	f.aliases[in.AliasName] = k.ID
	return map[string]any{}, nil
}

func (f *Fake) describeKey(body []byte) (any, error) {
	var in struct {
		KeyID string `json:"KeyId"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, errorf("ValidationException", "bad request: %v", err)
	}
	k, err := f.resolve(in.KeyID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"KeyMetadata": k.metadata()}, nil
}

func (f *Fake) getPublicKey(body []byte) (any, error) {
	var in struct {
		KeyID string `json:"KeyId"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, errorf("ValidationException", "bad request: %v", err)
	}
	k, err := f.resolve(in.KeyID)
	if err != nil {
		return nil, err
	}
	if err := f.usable(k); err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(k.priv.Public())
	if err != nil {
		return nil, err
	}
	return map[string]any{"KeyId": k.ARN(), "PublicKey": der, "KeySpec": k.Spec, "KeyUsage": k.Usage}, nil
}

func (f *Fake) sign(body []byte) (any, error) {
	var in struct {
		KeyID            string `json:"KeyId"`
		Message          []byte
		MessageType      string
		SigningAlgorithm string
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, errorf("ValidationException", "bad request: %v", err)
	}
	k, err := f.resolve(in.KeyID)
	if err != nil {
		return nil, err
	}
	if err := f.usable(k); err != nil {
		return nil, err
	}
	if k.Usage != "SIGN_VERIFY" {
		return nil, errorf("InvalidKeyUsageException", "the key usage is %s", k.Usage)
	}
	if in.MessageType != "DIGEST" {
		return nil, errorf("ValidationException", "the fake signs digests only")
	}
	var sig []byte
	switch in.SigningAlgorithm {
	case "ECDSA_SHA_384", "ECDSA_SHA_256":
		want, spec := 48, "ECC_NIST_P384"
		if in.SigningAlgorithm == "ECDSA_SHA_256" {
			want, spec = 32, "ECC_NIST_P256"
		}
		if k.Spec != spec {
			return nil, errorf("InvalidKeyUsageException", "%s does not go with %s", in.SigningAlgorithm, k.Spec)
		}
		if len(in.Message) != want {
			return nil, errorf("ValidationException", "a digest for %s is %d bytes", in.SigningAlgorithm, want)
		}
		sig, err = ecdsa.SignASN1(rand.Reader, k.priv.(*ecdsa.PrivateKey), in.Message)
	case "RSASSA_PKCS1_V1_5_SHA_256":
		if k.Spec != "RSA_2048" {
			return nil, errorf("InvalidKeyUsageException", "%s does not go with %s", in.SigningAlgorithm, k.Spec)
		}
		if len(in.Message) != 32 {
			return nil, errorf("ValidationException", "a digest for %s is 32 bytes", in.SigningAlgorithm)
		}
		sig, err = rsa.SignPKCS1v15(rand.Reader, k.priv.(*rsa.PrivateKey), crypto.SHA256, in.Message)
	default:
		return nil, errorf("ValidationException", "the fake does not know the signing algorithm %q", in.SigningAlgorithm)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"KeyId": k.ARN(), "Signature": sig, "SigningAlgorithm": in.SigningAlgorithm}, nil
}

func (f *Fake) deriveSharedSecret(body []byte) (any, error) {
	var in struct {
		KeyID                 string `json:"KeyId"`
		KeyAgreementAlgorithm string
		PublicKey             []byte
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, errorf("ValidationException", "bad request: %v", err)
	}
	k, err := f.resolve(in.KeyID)
	if err != nil {
		return nil, err
	}
	if err := f.usable(k); err != nil {
		return nil, err
	}
	if k.Usage != "KEY_AGREEMENT" {
		return nil, errorf("InvalidKeyUsageException", "the key usage is %s", k.Usage)
	}
	if in.KeyAgreementAlgorithm != "ECDH" {
		return nil, errorf("ValidationException", "the key agreement algorithm is ECDH")
	}
	parsed, err := x509.ParsePKIXPublicKey(in.PublicKey)
	if err != nil {
		return nil, errorf("ValidationException", "the public key is not SubjectPublicKeyInfo in DER")
	}
	var peer *ecdh.PublicKey
	switch p := parsed.(type) {
	case *ecdsa.PublicKey:
		peer, err = p.ECDH()
	case *ecdh.PublicKey:
		peer = p
	default:
		err = fmt.Errorf("not an EC key")
	}
	if err != nil {
		return nil, errorf("ValidationException", "the public key is not an EC key")
	}
	own, err := k.priv.(*ecdsa.PrivateKey).ECDH()
	if err != nil {
		return nil, err
	}
	secret, err := own.ECDH(peer)
	if err != nil {
		return nil, errorf("ValidationException", "the public key is not on the curve of the key")
	}
	return map[string]any{"KeyId": k.ARN(), "SharedSecret": secret, "KeyAgreementAlgorithm": "ECDH"}, nil
}

func (f *Fake) scheduleKeyDeletion(body []byte) (any, error) {
	var in struct {
		KeyID               string `json:"KeyId"`
		PendingWindowInDays int
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, errorf("ValidationException", "bad request: %v", err)
	}
	k, err := f.resolve(in.KeyID)
	if err != nil {
		return nil, err
	}
	days := in.PendingWindowInDays
	if days == 0 {
		days = 30
	}
	if days < 7 || days > 30 {
		return nil, errorf("ValidationException", "the pending window is 7 to 30 days")
	}
	k.State, k.DeletionDays = "PendingDeletion", days
	return map[string]any{"KeyId": k.ARN(), "KeyState": "PendingDeletion", "PendingWindowInDays": days}, nil
}
