package appconfig_test

// The tests of the driver run through the real AWS SDK client of the AppConfig data plane, talking to a fake of it over HTTP
// (internal/appconfigfake), and through the driver's Connect handler on an in-memory transport, as a Go consumer links it. No AWS account and
// no network are used. The conformance kit of the source port runs against the thin main over the same fake (tools/scripts/conformance-source.sh).

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/service/appconfigdata"

	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/refusal"
	"github.com/truvity/atoom/source/appconfig"
	"github.com/truvity/atoom/source/appconfig/internal/appconfigfake"
	sourcev1alpha1 "github.com/truvity/atoom/source/gen/atoom/source/v1alpha1"
	"github.com/truvity/atoom/source/gen/atoom/source/v1alpha1/sourcev1alpha1connect"
)

const (
	app = "example-app"
	env = "example-env"
)

// clock is a clock that a test moves.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type rig struct {
	fake   *appconfigfake.Fake
	clock  *clock
	client sourcev1alpha1connect.SourceServiceClient
	svc    *appconfig.Service
}

// start serves the driver over a fake with the options opts (the client, the application, the environment and the clock are filled in) on an
// in-memory transport.
func start(t *testing.T, opts appconfig.Options) *rig {
	t.Helper()
	f := appconfigfake.New()
	api, stop := f.Serve()
	t.Cleanup(stop)
	c := &clock{t: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
	opts.AppConfig, opts.Application, opts.Environment, opts.Now = api, app, env, c.now
	svc, err := appconfig.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	path, h := svc.Handler()
	hc := inMemory(t, path, h)
	return &rig{fake: f, clock: c, svc: svc, client: sourcev1alpha1connect.NewSourceServiceClient(hc, "http://source", call.ClientOptions()...)}
}

func (r *rig) read(name, held string) (*sourcev1alpha1.ReadResponse, error) {
	res, err := r.client.Read(context.Background(), connect.NewRequest(&sourcev1alpha1.ReadRequest{Name: name, IfNoneMatch: held}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func sha384(b []byte) string {
	s := sha512.Sum384(b)
	return hex.EncodeToString(s[:])
}

func wantRefusal(t *testing.T, err error, code connect.Code, reason string) {
	t.Helper()
	if connect.CodeOf(err) != code {
		t.Fatalf("code %v (%v), want %v", connect.CodeOf(err), err, code)
	}
	if got, _ := refusal.Reason(err); got != reason {
		t.Fatalf("reason %q, want %q", got, reason)
	}
	if reason != "engine" {
		if field, _ := refusal.FieldPath(err); field != "name" {
			t.Fatalf("field %q, want name", field)
		}
	}
}

func TestCapabilitiesSaysPollAndWatchIsUnimplemented(t *testing.T) {
	r := start(t, appconfig.Options{})
	res, err := r.client.Capabilities(context.Background(), connect.NewRequest(&sourcev1alpha1.CapabilitiesRequest{}))
	if err != nil || res.Msg.GetWatch() != sourcev1alpha1.Watch_POLL {
		t.Fatalf("capabilities: %v %v, want POLL", res, err)
	}
	stream, err := r.client.Watch(context.Background(), connect.NewRequest(&sourcev1alpha1.SourceWatchRequest{Name: "site"}))
	if err == nil {
		for stream.Receive() {
			t.Fatal("a watch said something")
		}
		err = stream.Err()
	}
	wantRefusal(t, err, connect.CodeUnimplemented, "rule")
}

func TestADocumentIsReadWithItsLabelAndPollAfter(t *testing.T) {
	r := start(t, appconfig.Options{})
	doc := []byte(`{"payload":"site","signatures":[]}`)
	r.fake.Deploy(app, env, "site", doc)

	first, err := r.read("site", "")
	if err != nil || !bytes.Equal(first.GetData(), doc) || first.GetUnchanged() {
		t.Fatalf("first read: %v %v", first, err)
	}
	if first.GetVersion() != sha384(doc) {
		t.Fatalf("label %q, want the SHA-384 of the bytes", first.GetVersion())
	}
	if d := first.GetPollAfter().AsDuration(); d != 60*time.Second {
		t.Fatalf("poll_after %v, want AppConfig's default interval of 60 s", d)
	}
	held, err := r.read("site", first.GetVersion())
	if err != nil || !held.GetUnchanged() || len(held.GetData()) != 0 || held.GetVersion() != first.GetVersion() {
		t.Fatalf("read at the label held: %v %v", held, err)
	}
	other, err := r.read("site", "another")
	if err != nil || other.GetUnchanged() || !bytes.Equal(other.GetData(), doc) {
		t.Fatalf("read at another label: %v %v", other, err)
	}
}

// Between two polls AppConfig is not asked; when the interval is over, one poll is made, with the token of the last answer.
func TestAppConfigIsPolledOnlyWhenTheIntervalIsOver(t *testing.T) {
	r := start(t, appconfig.Options{PollInterval: 30 * time.Second})
	r.fake.Deploy(app, env, "site", []byte("one"))
	first, err := r.read("site", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := first.GetPollAfter().AsDuration(); got != 30*time.Second {
		t.Fatalf("poll_after %v, want the interval asked for, 30 s", got)
	}
	r.clock.add(20 * time.Second)
	r.fake.Deploy(app, env, "site", []byte("two"))
	mid, err := r.read("site", "")
	if err != nil || string(mid.GetData()) != "one" {
		t.Fatalf("read inside the interval: %v %v, want the bytes of the last poll", mid, err)
	}
	if got := mid.GetPollAfter().AsDuration(); got != 10*time.Second {
		t.Fatalf("poll_after inside the interval %v, want what is left, 10 s", got)
	}
	if n := r.fake.Calls("GetLatestConfiguration"); n != 1 {
		t.Fatalf("%d polls inside the interval, want 1", n)
	}
	r.clock.add(10 * time.Second)
	after, err := r.read("site", first.GetVersion())
	if err != nil || after.GetUnchanged() || string(after.GetData()) != "two" || after.GetVersion() != sha384([]byte("two")) {
		t.Fatalf("read after the interval: %v %v, want the new deployment", after, err)
	}
	if n, s := r.fake.Calls("GetLatestConfiguration"), r.fake.Calls("StartConfigurationSession"); n != 2 || s != 1 {
		t.Fatalf("%d polls and %d sessions, want 2 and 1", n, s)
	}
	// A poll that brings nothing new (AppConfig's empty answer) keeps the bytes and the label.
	r.clock.add(30 * time.Second)
	same, err := r.read("site", "")
	if err != nil || string(same.GetData()) != "two" || same.GetVersion() != after.GetVersion() {
		t.Fatalf("read after an empty poll: %v %v", same, err)
	}
}

// The driver follows a longer interval that AppConfig answers.
func TestTheIntervalAppConfigAnswersIsFollowed(t *testing.T) {
	r := start(t, appconfig.Options{PollInterval: 15 * time.Second})
	r.fake.SetInterval(300)
	r.fake.Deploy(app, env, "site", []byte("one"))
	res, err := r.read("site", "")
	if err != nil || res.GetPollAfter().AsDuration() != 300*time.Second {
		t.Fatalf("read: %v %v, want poll_after 300 s", res, err)
	}
}

func TestEachNameHasItsOwnSessionAndAProfileMayBeMapped(t *testing.T) {
	r := start(t, appconfig.Options{Profiles: map[string]string{"policy-bundle": "abc1234"}})
	r.fake.Deploy(app, env, "site", []byte("site"))
	r.fake.Deploy(app, env, "abc1234", []byte("bundle"))
	r.fake.Deploy(app, env, "policy-bundle", []byte("not this one"))
	s, err := r.read("site", "")
	if err != nil || string(s.GetData()) != "site" {
		t.Fatalf("site: %v %v", s, err)
	}
	b, err := r.read("policy-bundle", "")
	if err != nil || string(b.GetData()) != "bundle" {
		t.Fatalf("policy-bundle: %v %v, want the mapped profile", b, err)
	}
	if n := r.fake.Calls("StartConfigurationSession"); n != 2 {
		t.Fatalf("%d sessions, want one for each name", n)
	}
}

func TestEveryRefusalNamesAReason(t *testing.T) {
	r := start(t, appconfig.Options{})
	for _, bad := range []string{"", "../x", "a/b", "a\x00b", "Upper", "1st", strings.Repeat("a", 64), "a.b"} {
		_, err := r.read(bad, "")
		wantRefusal(t, err, connect.CodeInvalidArgument, "schema")
	}
	if n := r.fake.Calls("StartConfigurationSession"); n != 0 {
		t.Fatalf("a malformed name reached AppConfig (%d calls)", n)
	}
	_, err := r.read("nothing", "")
	wantRefusal(t, err, connect.CodeNotFound, "absent")

	// A profile with nothing deployed in the environment is absent too.
	r.fake.Deploy(app, env, "empty", nil)
	_, err = r.read("empty", "")
	wantRefusal(t, err, connect.CodeNotFound, "absent")

	// Over the limit.
	r.fake.Deploy(app, env, "large", bytes.Repeat([]byte("x"), appconfig.DocumentMax+1))
	_, err = r.read("large", "")
	wantRefusal(t, err, connect.CodeFailedPrecondition, "bound")
	// At the limit.
	r.fake.Deploy(app, env, "limit", bytes.Repeat([]byte("x"), appconfig.DocumentMax))
	if res, err := r.read("limit", ""); err != nil || len(res.GetData()) != appconfig.DocumentMax {
		t.Fatalf("a document at the limit: %v", err)
	}
}

// A failure of AppConfig is unavailable/engine, and its text stays out of the answer; the next read starts a new session and recovers.
func TestAFailureOfAppConfigIsEngineAndTheNextReadRecovers(t *testing.T) {
	for _, op := range []string{"StartConfigurationSession", "GetLatestConfiguration"} {
		t.Run(op, func(t *testing.T) {
			r := start(t, appconfig.Options{})
			r.fake.Deploy(app, env, "site", []byte("one"))
			r.fake.Fail(op, "InternalServerException")
			_, err := r.read("site", "")
			wantRefusal(t, err, connect.CodeUnavailable, "engine")
			if strings.Contains(err.Error(), "fake") || strings.Contains(err.Error(), "Internal") {
				t.Fatalf("AppConfig's text reached the caller: %v", err)
			}
			r.fake.Fail(op, "")
			res, err := r.read("site", "")
			if err != nil || string(res.GetData()) != "one" {
				t.Fatalf("read after the failure: %v %v", res, err)
			}
		})
	}
	// Throttling is the backend's failure too.
	r := start(t, appconfig.Options{})
	r.fake.Deploy(app, env, "site", []byte("one"))
	r.fake.Fail("StartConfigurationSession", "ThrottlingException")
	_, err := r.read("site", "")
	wantRefusal(t, err, connect.CodeUnavailable, "engine")
}

// A token lives 24 hours; a poll with one that is no longer good starts a new session, once, and the caller sees nothing of it.
func TestATokenThatIsNoLongerGoodStartsANewSession(t *testing.T) {
	r := start(t, appconfig.Options{})
	r.fake.Deploy(app, env, "site", []byte("one"))
	first, err := r.read("site", "")
	if err != nil {
		t.Fatal(err)
	}
	r.fake.ExpireTokens()
	r.clock.add(time.Minute)
	again, err := r.read("site", "")
	if err != nil || string(again.GetData()) != "one" || again.GetVersion() != first.GetVersion() {
		t.Fatalf("read after the token expired: %v %v", again, err)
	}
	if n := r.fake.Calls("StartConfigurationSession"); n != 2 {
		t.Fatalf("%d sessions, want 2", n)
	}
	// A new session whose first poll is refused the same way is not tried a third time.
	r.fake.Fail("GetLatestConfiguration", "BadRequestException")
	r.clock.add(time.Minute)
	_, err = r.read("site", "")
	wantRefusal(t, err, connect.CodeUnavailable, "engine")
	if n := r.fake.Calls("StartConfigurationSession"); n != 3 {
		t.Fatalf("%d sessions, want 3 (one new session, no loop)", n)
	}
}

// A profile that is removed while it is read is absent from the next poll on.
func TestAProfileThatGoesAwayIsAbsent(t *testing.T) {
	r := start(t, appconfig.Options{})
	r.fake.Deploy(app, env, "site", []byte("one"))
	if _, err := r.read("site", ""); err != nil {
		t.Fatal(err)
	}
	r.fake.Remove(app, env, "site")
	r.clock.add(time.Minute)
	_, err := r.read("site", "")
	wantRefusal(t, err, connect.CodeNotFound, "absent")
}

func TestTheOptionsAreChecked(t *testing.T) {
	f := appconfigfake.New()
	api, stop := f.Serve()
	defer stop()
	for name, opts := range map[string]appconfig.Options{
		"no client":            {Application: app, Environment: env},
		"no application":       {AppConfig: api, Environment: env},
		"no environment":       {AppConfig: api, Application: app},
		"an interval too low":  {AppConfig: api, Application: app, Environment: env, PollInterval: 14 * time.Second},
		"an interval too high": {AppConfig: api, Application: app, Environment: env, PollInterval: 25 * time.Hour},
		"a part of a second":   {AppConfig: api, Application: app, Environment: env, PollInterval: 15500 * time.Millisecond},
		"a bad mapped name":    {AppConfig: api, Application: app, Environment: env, Profiles: map[string]string{"Bad": "x"}},
		"an empty mapped id":   {AppConfig: api, Application: app, Environment: env, Profiles: map[string]string{"site": ""}},
	} {
		if _, err := appconfig.New(opts); !errors.Is(err, appconfig.ErrOptions) {
			t.Errorf("%s: %v, want ErrOptions", name, err)
		}
	}
}

// seen wraps the client and keeps the correlation id of the context of each call, as the driver passes it on.
type seen struct {
	appconfig.API
	mu  sync.Mutex
	ids []string
}

func (s *seen) StartConfigurationSession(ctx context.Context, in *appconfigdata.StartConfigurationSessionInput, opts ...func(*appconfigdata.Options)) (*appconfigdata.StartConfigurationSessionOutput, error) {
	s.mu.Lock()
	s.ids = append(s.ids, call.CorrelationID(ctx))
	s.mu.Unlock()
	return s.API.StartConfigurationSession(ctx, in, opts...)
}

// The deadline and the correlation id of the caller reach the driver in process, and the calls it makes to AppConfig.
func TestTheCallContextReachesTheDriver(t *testing.T) {
	f := appconfigfake.New()
	api, stop := f.Serve()
	t.Cleanup(stop)
	f.Deploy(app, env, "site", []byte("one"))
	w := &seen{API: api}
	svc, err := appconfig.New(appconfig.Options{AppConfig: w, Application: app, Environment: env})
	if err != nil {
		t.Fatal(err)
	}
	path, h := svc.Handler()
	c := sourcev1alpha1connect.NewSourceServiceClient(inMemory(t, path, h), "http://source", call.ClientOptions()...)
	ctx, cancel := context.WithTimeout(call.WithCorrelationID(context.Background(), "corr-0001"), 5*time.Second)
	defer cancel()
	res, err := c.Read(ctx, connect.NewRequest(&sourcev1alpha1.ReadRequest{Name: "site"}))
	if err != nil || string(res.Msg.GetData()) != "one" {
		t.Fatalf("read with a deadline and an id: %v %v", res, err)
	}
	if len(w.ids) != 1 || w.ids[0] != "corr-0001" {
		t.Fatalf("the ids AppConfig was called under: %q, want corr-0001", w.ids)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if _, err := c.Read(ctx, connect.NewRequest(&sourcev1alpha1.ReadRequest{Name: "site"})); connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Fatalf("a read past its deadline: %v", err)
	}
}
