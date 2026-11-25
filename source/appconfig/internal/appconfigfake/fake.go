// Package appconfigfake is a fake of the AWS AppConfig data plane (the service appconfigdata) for the tests of the driver: an HTTP handler
// that speaks its REST-JSON protocol (POST /configurationsessions, GET /configuration?configuration_token=...), so that the driver is tested
// through the real AWS SDK client, its serialisation included. It holds deployed configurations by application, environment and profile,
// hands out tokens that are good for one call each, answers an empty body to a session that already has the latest content, and can be
// told to fail a call or to expire every token.
//
// It is not an AppConfig emulator. It knows only the two operations the driver calls and checks only what the driver can get wrong; where
// it is unsure how the real service answers, the driver's page says so (source/appconfig/docs/appconfig.md).
package appconfigfake

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/appconfigdata"
)

// Region is the region of the clients the fake makes.
const Region = "us-east-1"

// Fake is the state of one fake AppConfig data plane. It is safe for concurrent use.
type Fake struct {
	mu       sync.Mutex
	deployed map[string][]byte  // application/environment/profile -> the content deployed
	tokens   map[string]session // a token not yet used -> the session it belongs to
	fail     map[string]string  // operation -> the error type to answer
	calls    map[string]int
	interval int32 // the Next-Poll-Interval-In-Seconds of every answer, 0 = the session's minimum
}

// session is the state of one configuration session.
type session struct {
	key      string
	min      int32
	lastSent []byte // the content the session was last given; nil before the first
	sent     bool
}

// New returns an empty fake.
func New() *Fake {
	return &Fake{deployed: map[string][]byte{}, tokens: map[string]session{}, fail: map[string]string{}, calls: map[string]int{}}
}

func key(app, env, profile string) string { return app + "/" + env + "/" + profile }

// Deploy deploys data as the configuration of the profile in the application and environment. An empty data is a profile with nothing
// deployed: a session starts, and the first call gets no content.
func (f *Fake) Deploy(app, env, profile string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deployed[key(app, env, profile)] = append([]byte(nil), data...)
}

// Remove removes the profile: a session of it is refused as not found from then on.
func (f *Fake) Remove(app, env, profile string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.deployed, key(app, env, profile))
}

// Fail makes every call of the operation (StartConfigurationSession or GetLatestConfiguration) answer the error type errType
// (ThrottlingException, InternalServerException, BadRequestException, ResourceNotFoundException); an empty errType stops it.
func (f *Fake) Fail(op, errType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if errType == "" {
		delete(f.fail, op)
		return
	}
	f.fail[op] = errType
}

// ExpireTokens makes every token handed out so far unusable, as a token is after 24 hours.
func (f *Fake) ExpireTokens() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = map[string]session{}
}

// SetInterval sets the poll interval the fake answers, in seconds; 0 answers the session's minimum.
func (f *Fake) SetInterval(seconds int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interval = seconds
}

// Calls returns how many times the operation was called.
func (f *Fake) Calls(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

// Client returns a client of the real AWS SDK that talks to the fake at url, with placeholder credentials and no retry.
func Client(url string, hc *http.Client) *appconfigdata.Client {
	return appconfigdata.New(appconfigdata.Options{
		Region:       Region,
		BaseEndpoint: aws.String(url),
		HTTPClient:   hc,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "placeholder", SecretAccessKey: "placeholder"}, nil
		}),
		RetryMaxAttempts: 1,
	})
}

// Serve starts an HTTP server for the fake and returns a client of the real AWS SDK that talks to it, and the function that stops it.
func (f *Fake) Serve() (*appconfigdata.Client, func()) {
	srv := httptest.NewServer(f)
	return Client(srv.URL, srv.Client()), srv.Close
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// writeError answers an error of the type errType, as the service does: the type in the body's __type and the status of the type.
func writeError(w http.ResponseWriter, errType, msg string) {
	status := http.StatusBadRequest
	switch errType {
	case "ResourceNotFoundException":
		status = http.StatusNotFound
	case "ThrottlingException":
		status = http.StatusTooManyRequests
	case "InternalServerException":
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"__type": errType, "Message": msg})
}

// ServeHTTP is the data plane.
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/configurationsessions":
		f.start(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/configuration":
		f.latest(w, r)
	default:
		writeError(w, "BadRequestException", "the fake knows no such operation")
	}
}

func (f *Fake) start(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ApplicationIdentifier                string
		EnvironmentIdentifier                string
		ConfigurationProfileIdentifier       string
		RequiredMinimumPollIntervalInSeconds *int32
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, "BadRequestException", "the body is not JSON")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["StartConfigurationSession"]++
	if t, ok := f.fail["StartConfigurationSession"]; ok {
		writeError(w, t, "the fake was told to fail")
		return
	}
	if in.ApplicationIdentifier == "" || in.EnvironmentIdentifier == "" || in.ConfigurationProfileIdentifier == "" {
		writeError(w, "BadRequestException", "an identifier is missing")
		return
	}
	minimum := int32(60)
	if in.RequiredMinimumPollIntervalInSeconds != nil {
		minimum = *in.RequiredMinimumPollIntervalInSeconds
		if minimum < 15 || minimum > 86400 {
			writeError(w, "BadRequestException", "RequiredMinimumPollIntervalInSeconds is out of range")
			return
		}
	}
	k := key(in.ApplicationIdentifier, in.EnvironmentIdentifier, in.ConfigurationProfileIdentifier)
	if _, ok := f.deployed[k]; !ok {
		writeError(w, "ResourceNotFoundException", "no such configuration profile")
		return
	}
	tok := newToken()
	f.tokens[tok] = session{key: k, min: minimum}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"InitialConfigurationToken": tok})
}

func (f *Fake) latest(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("configuration_token")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["GetLatestConfiguration"]++
	if t, ok := f.fail["GetLatestConfiguration"]; ok {
		writeError(w, t, "the fake was told to fail")
		return
	}
	s, ok := f.tokens[tok]
	if !ok {
		writeError(w, "BadRequestException", "the token is not valid or has expired")
		return
	}
	delete(f.tokens, tok) // a token is good for one call
	data, there := f.deployed[s.key]
	if !there {
		writeError(w, "ResourceNotFoundException", "no such configuration profile")
		return
	}
	var body []byte
	if !s.sent || string(s.lastSent) != string(data) {
		body = data
		s.lastSent = append([]byte(nil), data...)
		s.sent = true
	}
	next := newToken()
	f.tokens[next] = s
	interval := s.min
	if f.interval > 0 {
		interval = f.interval
	}
	w.Header().Set("Next-Poll-Configuration-Token", next)
	w.Header().Set("Next-Poll-Interval-In-Seconds", strconv.Itoa(int(interval)))
	w.Header().Set("Content-Type", "application/octet-stream")
	if len(body) > 0 {
		w.Header().Set("Version-Label", fmt.Sprintf("v%d", len(body)))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
