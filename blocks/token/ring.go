package token

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	tokenv1alpha1 "github.com/truvity/atoom/blocks/token/gen/atoom/token/v1alpha1"
	"github.com/truvity/atoom/common/refusal"
	storev1alpha1 "github.com/truvity/atoom/store/gen/atoom/store/v1alpha1"
)

// The limits of a ring.
const (
	// MaxDropped is how many tombstones a ring keeps; beyond it the oldest drop is forgotten, and a driver that presents that key again
	// adds it as a new key.
	MaxDropped = 256
	// MaxRingItem is the largest ring item the block writes (the strictest store driver, DynamoDB, takes 400 KB).
	MaxRingItem = 256 << 10
	// ringAttempts is how many times a lost race on the ring item is read and planned again.
	ringAttempts = 3
)

// The states of a ring key, as the proto names them.
const (
	statePre      = tokenv1alpha1.KeyState_KEY_STATE_PRE_PUBLISHED
	stateActive   = tokenv1alpha1.KeyState_KEY_STATE_ACTIVE
	stateRetiring = tokenv1alpha1.KeyState_KEY_STATE_RETIRING
	stateDropped  = tokenv1alpha1.KeyState_KEY_STATE_DROPPED
	stateRevoked  = tokenv1alpha1.KeyState_KEY_STATE_REVOKED
)

// errRing is a ring that cannot be read or written; errNotActive is a node with no active key of a ring purpose.
var (
	errRing      = errors.New("token: the ring cannot be read or written")
	errNotActive = errors.New("token: this node holds no active key of the ring")
)

// published is whether a key of this state is in the key set.
func published(s tokenv1alpha1.KeyState) bool {
	return s == statePre || s == stateActive || s == stateRetiring
}

// verifies is whether a token of a key of this state verifies: one that signs or signed.
func verifies(s tokenv1alpha1.KeyState) bool { return s == stateActive || s == stateRetiring }

func (b *Block) now() time.Time {
	if b.opts.Clock != nil {
		return b.opts.Clock()
	}
	return time.Now()
}

func ms(t time.Time) int64 { return t.UnixMilli() }

// slotKey is the key that one slot of this node holds.
type slotKey struct {
	slot string
	c    *currentKey
}

// observe is the keys of a ring purpose's slots, as the block keeps them (read when it has none, or read again with fresh).
func (b *Block) observe(ctx context.Context, p Purpose, fresh bool) ([]slotKey, error) {
	var out []slotKey
	for _, s := range p.Ring.slots(p.Name) {
		read := b.cachedSlot
		if fresh {
			read = b.refreshSlot
		}
		c, err := read(ctx, p, s)
		if err != nil {
			return nil, err
		}
		out = append(out, slotKey{slot: s, c: c})
	}
	return out, nil
}

// find is the index of the ring key with this key_id, or else this kid; -1 when there is none.
func find(st *tokenv1alpha1.RingState, kid, keyID string) int {
	if keyID != "" {
		if i := slices.IndexFunc(st.GetKeys(), func(k *tokenv1alpha1.RingKey) bool { return k.GetKeyId() == keyID }); i >= 0 {
			return i
		}
	}
	return slices.IndexFunc(st.GetKeys(), func(k *tokenv1alpha1.RingKey) bool { return k.GetKid() == kid })
}

// change is one change a plan made.
type change struct {
	kid    string
	what   string
	state  tokenv1alpha1.KeyState
	reason string
}

func enter(k *tokenv1alpha1.RingKey, s tokenv1alpha1.KeyState, now time.Time) {
	k.State = s
	k.StateSinceUnixMs = ms(now)
}

// plan applies the rules of record 0024 to st, in place, for node, with the keys its slots hold now, and says what changed. Nothing is
// read or written here.
func plan(st *tokenv1alpha1.RingState, p Purpose, node string, held []slotKey, now time.Time) []change {
	r := p.Ring
	cur := r.current(p.Name)
	var out []change
	add := func(k *tokenv1alpha1.RingKey, what, reason string) {
		out = append(out, change{kid: k.GetKid(), what: what, state: k.GetState(), reason: reason})
	}
	anyActive := func() bool {
		return slices.ContainsFunc(st.GetKeys(), func(k *tokenv1alpha1.RingKey) bool { return k.GetState() == stateActive })
	}
	at := func(t int64) time.Time { return time.UnixMilli(t) }
	var current *currentKey
	for _, h := range held {
		if h.slot == cur {
			current = h.c
		}
	}
	// Whether this node holds an active key in a slot other than the current one.
	otherActive := slices.ContainsFunc(held, func(h slotKey) bool {
		if h.slot == cur {
			return false
		}
		i := find(st, h.c.kid, h.c.keyID)
		return i >= 0 && st.Keys[i].GetState() == stateActive
	})

	// 1. The current slot's key joins.
	if current != nil {
		i := find(st, current.kid, current.keyID)
		if i < 0 {
			nodeKnown := slices.ContainsFunc(st.GetKeys(), func(k *tokenv1alpha1.RingKey) bool { return k.GetNode() == node && k.GetState() != stateRevoked })
			young := r.Bootstrap > 0 && !slices.ContainsFunc(st.GetKeys(), func(k *tokenv1alpha1.RingKey) bool {
				return !now.Before(at(k.GetAddedUnixMs()).Add(r.Bootstrap))
			})
			shortcut := !anyActive()
			firstDeploy := !nodeKnown && young
			atOnce := r.AtOnce && !otherActive
			state, reason := statePre, ""
			if !r.Confirm && (shortcut || firstDeploy || atOnce) {
				state, reason = stateActive, ReasonShortcut
			}
			k := &tokenv1alpha1.RingKey{
				Kid: current.kid, KeyId: current.keyID, Algorithm: p.Algorithm, SpkiDer: current.key.spki, Node: node, Slot: cur,
				State: state, AddedUnixMs: ms(now), StateSinceUnixMs: ms(now),
			}
			st.Keys = append(st.Keys, k)
			add(k, ChangeJoined, reason)
		} else if k := st.Keys[i]; k.GetState() == statePre && k.GetNode() == node {
			// 2. It becomes active after its lead, or at once when no key of the ring is active.
			basis, confirmed := at(k.GetStateSinceUnixMs()), true
			if r.Confirm {
				basis, confirmed = at(k.GetConfirmedUnixMs()), k.GetConfirmedUnixMs() != 0
			}
			lead := confirmed && !now.Before(basis.Add(r.Lead))
			short := confirmed && !anyActive()
			if lead || short {
				enter(k, stateActive, now)
				reason := ""
				if !lead {
					reason = ReasonShortcut
				}
				add(k, ChangeActivated, reason)
			}
		}
	}

	// 3. Once the current key is active, the node's keys that joined before it became active retire: a pre-published one at once, an
	// active one after the overlap.
	if current != nil {
		if i := find(st, current.kid, current.keyID); i >= 0 && st.Keys[i].GetState() == stateActive && st.Keys[i].GetNode() == node {
			newest := st.Keys[i]
			since := newest.GetStateSinceUnixMs()
			for _, k := range st.GetKeys() {
				if k == newest || k.GetNode() != node || k.GetAddedUnixMs() >= since {
					continue
				}
				if k.GetState() == statePre || (k.GetState() == stateActive && !now.Before(at(since).Add(r.Overlap))) {
					enter(k, stateRetiring, now)
					add(k, ChangeRetired, "")
				}
			}
		}
	}

	// 4. A retiring key is dropped after the retention, by any node: a tombstone stays.
	for _, k := range st.GetKeys() {
		if k.GetState() == stateRetiring && !now.Before(at(k.GetStateSinceUnixMs()).Add(r.Retain)) {
			enter(k, stateDropped, now)
			k.SpkiDer = nil
			add(k, ChangeDropped, "")
		}
	}
	var drops []*tokenv1alpha1.RingKey
	for _, k := range st.GetKeys() {
		if k.GetState() == stateDropped {
			drops = append(drops, k)
		}
	}
	if len(drops) > MaxDropped {
		sort.SliceStable(drops, func(i, j int) bool { return drops[i].GetStateSinceUnixMs() < drops[j].GetStateSinceUnixMs() })
		forget := drops[:len(drops)-MaxDropped]
		st.Keys = slices.DeleteFunc(st.Keys, func(k *tokenv1alpha1.RingKey) bool { return slices.Contains(forget, k) })
		for _, k := range forget {
			add(k, ChangeForgotten, "")
		}
	}
	return out
}

// signing is the ring key this node signs with, and its slot: the current slot's key when it is active, or else another slot's key that
// is active. None is errNotActive.
func signing(st *tokenv1alpha1.RingState, p Purpose, held []slotKey) (*tokenv1alpha1.RingKey, slotKey, error) {
	cur := p.Ring.current(p.Name)
	order := slices.Clone(held)
	slices.SortStableFunc(order, func(a, b slotKey) int {
		switch {
		case a.slot == cur && b.slot != cur:
			return -1
		case b.slot == cur && a.slot != cur:
			return 1
		}
		return 0
	})
	for _, h := range order {
		if i := find(st, h.c.kid, h.c.keyID); i >= 0 {
			k := st.Keys[i]
			if k.GetState() == stateActive && k.GetKeyId() == h.c.keyID && k.GetKid() == h.c.kid {
				return k, h, nil
			}
		}
	}
	return nil, slotKey{}, errNotActive
}

func (b *Block) ringKey(name string) []byte {
	prefix := b.opts.RingPrefix
	if prefix == "" {
		prefix = DefaultRingPrefix
	}
	return []byte(prefix + name)
}

// readRing reads the ring item of a purpose: linearizable, so that a revocation that another instance wrote is seen. An absent item is an
// empty ring at version 0.
func (b *Block) readRing(ctx context.Context, name string) (*tokenv1alpha1.RingState, uint64, error) {
	res, err := b.opts.Items.Get(ctx, connect.NewRequest(&storev1alpha1.ItemGetRequest{Key: b.ringKey(name), Linearizable: true}))
	switch {
	case err == nil:
	case connect.CodeOf(err) == connect.CodeNotFound:
		return &tokenv1alpha1.RingState{}, 0, nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil, 0, err
	default:
		return nil, 0, errRing
	}
	st := &tokenv1alpha1.RingState{}
	if proto.Unmarshal(res.Msg.GetItem().GetValue(), st) != nil {
		return nil, 0, errRing
	}
	return st, res.Msg.GetItem().GetVersion(), nil
}

// errConflict is a ring item that another instance changed in between.
var errConflict = errors.New("token: the ring changed in between")

// writeRing writes the ring item at the version read (absent at 0).
func (b *Block) writeRing(ctx context.Context, name string, st *tokenv1alpha1.RingState, version uint64) error {
	v, err := proto.MarshalOptions{Deterministic: true}.Marshal(st)
	if err != nil || len(v) > MaxRingItem {
		return errRing
	}
	key := b.ringKey(name)
	cond := &storev1alpha1.Condition{Key: key, Expect: &storev1alpha1.Condition_Absent{Absent: true}}
	if version != 0 {
		cond.Expect = &storev1alpha1.Condition_Version{Version: version}
	}
	_, err = b.opts.Items.Transact(ctx, connect.NewRequest(&storev1alpha1.TransactRequest{
		Conditions: []*storev1alpha1.Condition{cond},
		Puts:       []*storev1alpha1.Put{{Key: key, Value: v}},
	}))
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	if r, _ := refusal.Reason(err); connect.CodeOf(err) == connect.CodeAborted && r == "version" {
		return errConflict
	}
	return errRing
}

// mutate reads the ring of a purpose, lets fn change it, and writes it when fn changed something, with the version guard; a lost race
// reads and plans again. It reports the changes as facts.
func (b *Block) mutate(ctx context.Context, op Op, name string, fn func(*tokenv1alpha1.RingState) []change) (*tokenv1alpha1.RingState, []change, error) {
	for range ringAttempts {
		st, version, err := b.readRing(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		changes := fn(st)
		if len(changes) == 0 {
			return st, nil, nil
		}
		switch err := b.writeRing(ctx, name, st, version); {
		case err == nil:
			for _, c := range changes {
				b.observeEvent(ctx, Event{Action: EventRing, Op: op, Purpose: name, Kid: c.kid, Change: c.what, State: stateWord(c.state), Reason: c.reason})
			}
			return st, changes, nil
		case errors.Is(err, errConflict):
			continue
		default:
			return nil, nil, err
		}
	}
	return nil, nil, errRing
}

// reconcile brings the ring of a purpose in line with the keys its slots hold and the clock.
func (b *Block) reconcile(ctx context.Context, op Op, p Purpose, held []slotKey) (*tokenv1alpha1.RingState, []change, error) {
	now := b.now()
	return b.mutate(ctx, op, p.Name, func(st *tokenv1alpha1.RingState) []change { return plan(st, p, b.opts.Node, held, now) })
}

// stateWord is the state as the facts name it.
func stateWord(s tokenv1alpha1.KeyState) string {
	switch s {
	case statePre:
		return "pre_published"
	case stateActive:
		return "active"
	case stateRetiring:
		return "retiring"
	case stateDropped:
		return "dropped"
	case stateRevoked:
		return "revoked"
	}
	return "unspecified"
}

// ringFailure turns an error of a ring call into its refusal: the ring's own, or the seal block's.
func (b *Block) ringFailure(ctx context.Context, op Op, p string, err error) error {
	switch {
	case errors.Is(err, errRing):
		return b.refuse(ctx, op, p, connect.CodeUnavailable, ReasonRing, "")
	case errors.Is(err, errNotActive):
		return b.refuse(ctx, op, p, connect.CodeUnavailable, ReasonNotActive, "purpose")
	}
	return b.sealFailure(ctx, op, p, err)
}

// ringPurposes is the named purposes, each with a ring, or every ring purpose of the declaration.
func (b *Block) ringPurposes(ctx context.Context, op Op, names []string) ([]Purpose, error) {
	if len(names) == 0 {
		var out []Purpose
		for _, p := range b.opts.Purposes {
			if p.Ring != nil {
				out = append(out, p)
			}
		}
		return out, nil
	}
	ps, err := b.purposesOf(ctx, op, names)
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.Ring == nil {
			return nil, b.refuse(ctx, op, p.Name, connect.CodeInvalidArgument, ReasonPurpose, "purposes")
		}
	}
	return ps, nil
}

// purposeRing is the answer of one ring: its keys and the kid this instance signs with.
func purposeRing(p Purpose, st *tokenv1alpha1.RingState, held []slotKey) *tokenv1alpha1.PurposeRing {
	out := &tokenv1alpha1.PurposeRing{Purpose: p.Name}
	for _, k := range st.GetKeys() {
		out.Keys = append(out.Keys, proto.CloneOf(k))
	}
	if k, _, err := signing(st, p, held); err == nil {
		out.SigningKid = k.GetKid()
	}
	return out
}

// Ring answers the rings of ring purposes as they are, with the kid this instance would sign with. It writes nothing.
func (b *Block) Ring(ctx context.Context, req *connect.Request[tokenv1alpha1.RingRequest]) (*connect.Response[tokenv1alpha1.RingResponse], error) {
	ps, err := b.ringPurposes(ctx, OpRing, req.Msg.GetPurposes())
	if err != nil {
		return nil, err
	}
	if err := b.decide(ctx, Request{Op: OpRing}); err != nil {
		return nil, err
	}
	out := &tokenv1alpha1.RingResponse{}
	for _, p := range ps {
		held, err := b.observe(ctx, p, false)
		if err != nil {
			return nil, b.ringFailure(ctx, OpRing, p.Name, err)
		}
		st, _, err := b.readRing(ctx, p.Name)
		if err != nil {
			return nil, b.ringFailure(ctx, OpRing, p.Name, err)
		}
		out.Rings = append(out.Rings, purposeRing(p, st, held))
	}
	return connect.NewResponse(out), nil
}

// Reconcile reads the slots' keys again, applies the ring's rules now, and answers the rings and what changed: a host's loop calls it, so
// that an idle ring promotes and drops its keys on time.
func (b *Block) Reconcile(ctx context.Context, req *connect.Request[tokenv1alpha1.ReconcileRequest]) (*connect.Response[tokenv1alpha1.ReconcileResponse], error) {
	ps, err := b.ringPurposes(ctx, OpReconcile, req.Msg.GetPurposes())
	if err != nil {
		return nil, err
	}
	if err := b.decide(ctx, Request{Op: OpReconcile}); err != nil {
		return nil, err
	}
	out := &tokenv1alpha1.ReconcileResponse{}
	for _, p := range ps {
		held, err := b.observe(ctx, p, true)
		if err != nil {
			return nil, b.ringFailure(ctx, OpReconcile, p.Name, err)
		}
		st, changes, err := b.reconcile(ctx, OpReconcile, p, held)
		if err != nil {
			return nil, b.ringFailure(ctx, OpReconcile, p.Name, err)
		}
		out.Rings = append(out.Rings, purposeRing(p, st, held))
		for _, c := range changes {
			out.Changes = append(out.Changes, &tokenv1alpha1.RingChange{Purpose: p.Name, Kid: c.kid, Change: c.what, State: c.state, Reason: c.reason})
		}
	}
	return connect.NewResponse(out), nil
}

// ringPurpose is a declared purpose with a ring.
func (b *Block) ringPurpose(ctx context.Context, op Op, name string) (Purpose, error) {
	p, ok := b.purposes[name]
	if !ok || p.Ring == nil {
		return Purpose{}, b.refuse(ctx, op, name, connect.CodeInvalidArgument, ReasonPurpose, "purpose")
	}
	return p, nil
}

// Revoke revokes a kid of a ring purpose, at once and for good. A kid the ring does not hold is recorded as well, so that the key never
// joins. Who may revoke is the host's rules.
func (b *Block) Revoke(ctx context.Context, req *connect.Request[tokenv1alpha1.RevokeRequest]) (*connect.Response[tokenv1alpha1.RevokeResponse], error) {
	m := req.Msg
	p, err := b.ringPurpose(ctx, OpRevoke, m.GetPurpose())
	if err != nil {
		return nil, err
	}
	if !validKid(m.GetKid()) {
		return nil, b.refuse(ctx, OpRevoke, p.Name, connect.CodeInvalidArgument, ReasonKeys, "kid")
	}
	if err := b.decide(ctx, Request{Op: OpRevoke, Purpose: p.Name, Kid: m.GetKid()}); err != nil {
		return nil, err
	}
	now := b.now()
	_, changes, err := b.mutate(ctx, OpRevoke, p.Name, func(st *tokenv1alpha1.RingState) []change {
		i := slices.IndexFunc(st.GetKeys(), func(k *tokenv1alpha1.RingKey) bool { return k.GetKid() == m.GetKid() })
		if i < 0 {
			k := &tokenv1alpha1.RingKey{Kid: m.GetKid(), Algorithm: p.Algorithm, State: stateRevoked, AddedUnixMs: ms(now), StateSinceUnixMs: ms(now)}
			st.Keys = append(st.Keys, k)
			return []change{{kid: k.GetKid(), what: ChangeRevoked, state: stateRevoked}}
		}
		k := st.Keys[i]
		if k.GetState() == stateRevoked {
			return nil
		}
		enter(k, stateRevoked, now)
		k.SpkiDer = nil
		return []change{{kid: k.GetKid(), what: ChangeRevoked, state: stateRevoked}}
	})
	if err != nil {
		return nil, b.ringFailure(ctx, OpRevoke, p.Name, err)
	}
	return connect.NewResponse(&tokenv1alpha1.RevokeResponse{Changed: len(changes) > 0}), nil
}

// ConfirmPublished records the host's statement that every published copy of the key set has the kids since a time: the first
// confirmation of a pre-published key counts. It matters for a ring with Confirm.
func (b *Block) ConfirmPublished(ctx context.Context, req *connect.Request[tokenv1alpha1.ConfirmPublishedRequest]) (*connect.Response[tokenv1alpha1.ConfirmPublishedResponse], error) {
	m := req.Msg
	p, err := b.ringPurpose(ctx, OpConfirm, m.GetPurpose())
	if err != nil {
		return nil, err
	}
	if len(m.GetKids()) > MaxHostKeys || slices.ContainsFunc(m.GetKids(), func(k string) bool { return !validKid(k) }) {
		return nil, b.refuse(ctx, OpConfirm, p.Name, connect.CodeInvalidArgument, ReasonKeys, "kids")
	}
	now := b.now()
	at := now
	if m.GetAtUnixMs() != 0 {
		at = time.UnixMilli(m.GetAtUnixMs())
	}
	if at.After(now) || m.GetAtUnixMs() < 0 {
		return nil, b.refuse(ctx, OpConfirm, p.Name, connect.CodeInvalidArgument, ReasonTime, "at_unix_ms")
	}
	if err := b.decide(ctx, Request{Op: OpConfirm, Purpose: p.Name}); err != nil {
		return nil, err
	}
	_, changes, err := b.mutate(ctx, OpConfirm, p.Name, func(st *tokenv1alpha1.RingState) []change {
		var out []change
		for _, k := range st.GetKeys() {
			if k.GetState() == statePre && k.GetConfirmedUnixMs() == 0 && slices.Contains(m.GetKids(), k.GetKid()) {
				k.ConfirmedUnixMs = ms(at)
				out = append(out, change{kid: k.GetKid(), what: ChangeConfirmed, state: statePre})
			}
		}
		return out
	})
	if err != nil {
		return nil, b.ringFailure(ctx, OpConfirm, p.Name, err)
	}
	res := &tokenv1alpha1.ConfirmPublishedResponse{}
	for _, c := range changes {
		res.Confirmed = append(res.Confirmed, c.kid)
	}
	return connect.NewResponse(res), nil
}

// ringEntries are the keys of a ring purpose that a key set publishes (keep = published) or that a verification uses (keep = verifies),
// after a reconcile with the keys its slots hold, and the kid this node signs with.
func (b *Block) ringEntries(ctx context.Context, op Op, p Purpose, fresh bool, keep func(tokenv1alpha1.KeyState) bool) ([]entry, string, error) {
	held, err := b.observe(ctx, p, fresh)
	if err != nil {
		return nil, "", err
	}
	st, _, err := b.reconcile(ctx, op, p, held)
	if err != nil {
		return nil, "", err
	}
	var out []entry
	for _, k := range st.GetKeys() {
		if !keep(k.GetState()) {
			continue
		}
		pk, err := parsePublic(k.GetAlgorithm(), k.GetSpkiDer())
		if err != nil || pk.algorithm != p.Algorithm {
			return nil, "", errInconsistent
		}
		out = append(out, entry{kid: k.GetKid(), key: pk, purpose: p.Name, keyID: k.GetKeyId(), types: p.Types, state: k.GetState(), node: k.GetNode()})
	}
	signingKid := ""
	if k, _, err := signing(st, p, held); err == nil {
		signingKid = k.GetKid()
	}
	return out, signingKid, nil
}

// signRing signs by a ring purpose: the ring is reconciled with the slots' keys, and the key this node signs with is the current slot's
// active key or another slot's. A signature whose key_id is not the slot's kept key (a key replaced behind the block) reads the slot
// again, plans again and signs once more.
func (b *Block) signRing(ctx context.Context, p Purpose, typ string, claims, authorization []byte) (*tokenv1alpha1.SignResponse, error) {
	fresh := false
	for attempt := 0; ; attempt++ {
		held, err := b.observe(ctx, p, fresh)
		if err != nil {
			return nil, b.ringFailure(ctx, OpSign, p.Name, err)
		}
		st, _, err := b.reconcile(ctx, OpSign, p, held)
		if err != nil {
			return nil, b.ringFailure(ctx, OpSign, p.Name, err)
		}
		_, h, err := signing(st, p, held)
		if err != nil {
			return nil, b.ringFailure(ctx, OpSign, p.Name, err)
		}
		res, err := b.signWith(ctx, p, h.slot, h.c, typ, claims, authorization)
		if errors.Is(err, errReplaced) {
			if attempt > 0 {
				return nil, b.sealFailure(ctx, OpSign, p.Name, errInconsistent)
			}
			fresh = true
			continue
		}
		if err != nil {
			return nil, b.sealFailure(ctx, OpSign, p.Name, err)
		}
		return res, nil
	}
}

// keyChains checks the chains by kid against the set: a kid that is not a ring key of the set is refused.
func (b *Block) keyChains(ctx context.Context, set []entry, chains map[string]*tokenv1alpha1.CertificateChain) error {
	for kid, chain := range chains {
		at := slices.IndexFunc(set, func(e entry) bool { return e.kid == kid && e.state != 0 })
		if at < 0 || len(chain.GetX5C()) > MaxChain {
			return b.refuse(ctx, OpKeySet, "", connect.CodeInvalidArgument, ReasonKeys, "key_chains")
		}
		set[at].x5c = chain.GetX5C()
	}
	return nil
}
