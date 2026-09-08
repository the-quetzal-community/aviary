package nettest

// The mutation gate: every kind of musical mutation an editor can emit must
// (1) reach every peer in both directions, (2) persist iff it is a committed
// contribution, (3) reach a peer that joins late via catch-up, and (4) survive
// a host reload from the saved file. This is the executable form of the
// project rule "all mutations must be observable by all clients" and of the
// definition-of-done item "replicates to peers and survives save/reload".
//
// Adding a new mutation kind or a new persisted field? Add a row to the
// mutations table below. The rest of the checks derive from the table.

import (
	"io"
	"io/fs"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

const gateTimeout = 5 * time.Second

// kind selects which recorder channel a mutation lands on.
type kind int

const (
	kindImport kind = iota
	kindUpload
	kindChange
	kindAction
	kindSculpt
	kindLookAt
)

func (k kind) String() string {
	return [...]string{"Import", "Upload", "Change", "Action", "Sculpt", "LookAt"}[k]
}

func kindOf(e any) kind {
	switch e.(type) {
	case musical.Import:
		return kindImport
	case musical.Upload:
		return kindUpload
	case musical.Change:
		return kindChange
	case musical.Action:
		return kindAction
	case musical.Sculpt:
		return kindSculpt
	case musical.LookAt:
		return kindLookAt
	}
	return -1
}

// mutation is one row of the gate: how to emit it as a given author, and how
// to recognise the emitted record (on a replica or on disk) for that author.
type mutation struct {
	name    string
	kind    kind
	persist bool // committed contributions are written to the .mus3
	send    func(space musical.UsersSpace3D, author musical.Author) error
	is      func(e any, author musical.Author) bool
}

var mutations = []mutation{
	{
		name: "import", kind: kindImport, persist: true,
		send: func(s musical.UsersSpace3D, a musical.Author) error {
			return s.Import(musical.Import{Design: musical.Design{Author: a, Number: 1}, Import: "res://library/everything/avatar/bald_eagle.glb"})
		},
		is: func(e any, a musical.Author) bool {
			v, ok := e.(musical.Import)
			return ok && v.Design == musical.Design{Author: a, Number: 1} && strings.HasSuffix(v.Import, "bald_eagle.glb")
		},
	},
	{
		name: "upload bundle", kind: kindUpload, persist: true,
		send: func(s musical.UsersSpace3D, a musical.Author) error {
			return s.Upload(musical.Upload{Design: musical.Design{Author: a, Number: 2}, Bundle: []byte{1, 2, 3, byte(a)}})
		},
		is: func(e any, a musical.Author) bool {
			v, ok := e.(musical.Upload)
			return ok && v.Design == musical.Design{Author: a, Number: 2} && string(v.Bundle) == string([]byte{1, 2, 3, byte(a)})
		},
	},
	{
		name: "change commit", kind: kindChange, persist: true,
		send: func(s musical.UsersSpace3D, a musical.Author) error {
			return s.Change(musical.Change{
				Author: a, Entity: musical.Entity{Author: a, Number: 1}, Design: musical.Design{Author: a, Number: 1},
				Offset: Vector3.New(1, 2, 3), Editor: "scenery", Commit: true,
			})
		},
		is: func(e any, a musical.Author) bool {
			v, ok := e.(musical.Change)
			return ok && v.Author == a && v.Entity.Number == 1 && v.Commit && !v.Remove && v.Offset == Vector3.New(1, 2, 3)
		},
	},
	{
		name: "change preview", kind: kindChange, persist: false,
		send: func(s musical.UsersSpace3D, a musical.Author) error {
			return s.Change(musical.Change{
				Author: a, Entity: musical.Entity{Author: a, Number: 1}, Design: musical.Design{Author: a, Number: 1},
				Offset: Vector3.New(4, 5, 6), Editor: "scenery", Commit: false,
			})
		},
		is: func(e any, a musical.Author) bool {
			v, ok := e.(musical.Change)
			return ok && v.Author == a && v.Entity.Number == 1 && !v.Commit && v.Offset == Vector3.New(4, 5, 6)
		},
	},
	{
		name: "change remove", kind: kindChange, persist: true,
		send: func(s musical.UsersSpace3D, a musical.Author) error {
			return s.Change(musical.Change{
				Author: a, Entity: musical.Entity{Author: a, Number: 1}, Editor: "scenery", Remove: true, Commit: true,
			})
		},
		is: func(e any, a musical.Author) bool {
			v, ok := e.(musical.Change)
			return ok && v.Author == a && v.Entity.Number == 1 && v.Remove && v.Commit
		},
	},
	{
		name: "action commit", kind: kindAction, persist: true,
		send: func(s musical.UsersSpace3D, a musical.Author) error {
			return s.Action(musical.Action{
				Author: a, Entity: musical.Entity{Author: a, Number: 1}, Target: Vector3.New(7, 0, 7),
				Period: musical.Period(2 * time.Second), Editor: "critter", Commit: true,
			})
		},
		is: func(e any, a musical.Author) bool {
			v, ok := e.(musical.Action)
			return ok && v.Author == a && v.Commit && v.Target == Vector3.New(7, 0, 7) && v.Period == musical.Period(2*time.Second)
		},
	},
	{
		name: "action preview", kind: kindAction, persist: false,
		send: func(s musical.UsersSpace3D, a musical.Author) error {
			return s.Action(musical.Action{
				Author: a, Entity: musical.Entity{Author: a, Number: 1}, Target: Vector3.New(8, 0, 8), Editor: "critter", Commit: false,
			})
		},
		is: func(e any, a musical.Author) bool {
			v, ok := e.(musical.Action)
			return ok && v.Author == a && !v.Commit && v.Target == Vector3.New(8, 0, 8)
		},
	},
	{
		name: "sculpt commit", kind: kindSculpt, persist: true,
		send: func(s musical.UsersSpace3D, a musical.Author) error {
			return s.Sculpt(musical.Sculpt{
				Author: a, Target: Vector3.New(1, 0, 1), Radius: 3, Amount: 0.5, Editor: "terrain", Slider: "raise", Timing: 42, Commit: true,
			})
		},
		is: func(e any, a musical.Author) bool {
			v, ok := e.(musical.Sculpt)
			return ok && v.Author == a && v.Commit && !v.Revert && v.Slider == "raise" && v.Radius == 3 && v.Timing == 42
		},
	},
	{
		name: "sculpt preview", kind: kindSculpt, persist: false,
		send: func(s musical.UsersSpace3D, a musical.Author) error {
			return s.Sculpt(musical.Sculpt{
				Author: a, Target: Vector3.New(1, 0, 1), Radius: 3, Amount: 0.5, Editor: "terrain", Slider: "raise", Commit: false,
			})
		},
		is: func(e any, a musical.Author) bool {
			v, ok := e.(musical.Sculpt)
			return ok && v.Author == a && !v.Commit && !v.Revert && v.Slider == "raise"
		},
	},
	{
		name: "sculpt revert (undo)", kind: kindSculpt, persist: true,
		send: func(s musical.UsersSpace3D, a musical.Author) error {
			return s.Sculpt(musical.Sculpt{Author: a, Timing: 42, Editor: "terrain", Commit: true, Revert: true})
		},
		is: func(e any, a musical.Author) bool {
			v, ok := e.(musical.Sculpt)
			return ok && v.Author == a && v.Commit && v.Revert && v.Timing == 42
		},
	},
	{
		name: "lookat (presence)", kind: kindLookAt, persist: false,
		send: func(s musical.UsersSpace3D, a musical.Author) error {
			return s.LookAt(musical.LookAt{Author: a, Offset: Vector3.New(9, 9, 9), Editor: "scenery", Action: "wave"})
		},
		is: func(e any, a musical.Author) bool {
			v, ok := e.(musical.LookAt)
			return ok && v.Author == a && v.Offset == Vector3.New(9, 9, 9) && v.Action == "wave"
		},
	},
}

// expectedOnDisk is the persisted record sequence the table implies: for each
// persisted row, the host's record then the client's (the gate waits for the
// client to observe the host's record before the client sends its own, so the
// server's serial apply order is deterministic).
func expectedOnDisk(clientAuthor musical.Author) []struct {
	row    mutation
	author musical.Author
} {
	var out []struct {
		row    mutation
		author musical.Author
	}
	for _, m := range mutations {
		if !m.persist {
			continue
		}
		out = append(out,
			struct {
				row    mutation
				author musical.Author
			}{m, hostAuthor},
			struct {
				row    mutation
				author musical.Author
			}{m, clientAuthor})
	}
	return out
}

func TestMutationGate(t *testing.T) {
	var errs errSink
	store := &sharedStorage{}

	s := startHost(t, store, &errs)
	defer s.stop()
	p1 := s.join(t)
	defer p1.close()
	if p1.number != 0 {
		t.Fatalf("first joiner should catch up 0 records, got %d", p1.number)
	}

	// (1) Fan-out in both directions, one row per mutation kind.
	for _, m := range mutations {
		t.Run("fanout/"+m.name, func(t *testing.T) {
			if err := m.send(s.space, hostAuthor); err != nil {
				t.Fatalf("host send: %v", err)
			}
			p1.rec.expect(t, m, hostAuthor, "client sees host's "+m.name)
			if err := m.send(p1.space, p1.author); err != nil {
				t.Fatalf("client send: %v", err)
			}
			s.host.expect(t, m, p1.author, "host sees client's "+m.name)
		})
	}

	// (2) Authorship is enforced: a peer cannot write as someone else. The
	// forged record must not reach the host replica; the next honest one must.
	t.Run("forged author dropped", func(t *testing.T) {
		forged := musical.Change{Author: hostAuthor, Entity: musical.Entity{Author: hostAuthor, Number: 77}, Commit: true}
		if err := p1.space.Change(forged); err != nil {
			t.Fatalf("send forged: %v", err)
		}
		marker := musical.Change{Author: p1.author, Entity: musical.Entity{Author: p1.author, Number: 99}, Commit: true}
		if err := p1.space.Change(marker); err != nil {
			t.Fatalf("send marker: %v", err)
		}
		got := recvMatch(t, s.host.changes, gateTimeout, "host sees marker",
			func(c musical.Change) bool { return c.Entity.Number == 77 || c.Entity.Number == 99 })
		if got.Entity.Number != 99 {
			t.Fatalf("host accepted a forged Change: %+v", got)
		}
		if !errs.contains("invalid author") {
			t.Errorf("forged request was not reported as an invalid-author error")
		}
	})

	// (3) Persistence: exactly the committed contributions, in apply order.
	want := expectedOnDisk(p1.author)
	var disk []any
	t.Run("persisted iff committed", func(t *testing.T) {
		entries, err := musical.UnmarshalEntries(store.records())
		if err != nil {
			t.Fatalf("decode save: %v", err)
		}
		// Drop the marker Change from the forged-author check; it is the last record.
		if n := len(entries); n == 0 || !isMarker(entries[n-1]) {
			t.Fatalf("expected the honest marker Change to be the last saved record, got %d records", len(entries))
		}
		entries = entries[:len(entries)-1]
		if len(entries) != len(want) {
			t.Fatalf("saved %d records, want %d:\n%s", len(entries), len(want), describe(entries))
		}
		for i, w := range want {
			if !w.row.is(entries[i], w.author) {
				t.Errorf("record %d: want %s by author %d, got %#v", i, w.row.name, w.author, entries[i])
			}
		}
		disk = entries
	})
	if t.Failed() {
		return
	}
	total := uint64(len(disk) + 1) // + marker

	// (4) Late join: a new peer is told the record count and catches every one up.
	t.Run("late join catch-up", func(t *testing.T) {
		p2 := s.join(t)
		defer p2.close()
		if p2.number != total {
			t.Fatalf("late joiner told %d records, want %d", p2.number, total)
		}
		got := p2.rec.drain(t, int(total))
		checkReplay(t, got, want)
	})

	// (5) Reload: a fresh host on the same save replays the same records to its
	// replica, and a joiner to the reloaded host catches them up too.
	t.Run("reload from save", func(t *testing.T) {
		p1.close()
		s.stop()
		s2 := startHost(t, store, &errs)
		defer s2.stop()
		// The host replica receives the replay before its own Member.
		got := s2.host.drain(t, int(total))
		checkReplay(t, got, want)
		if s2.number != total {
			t.Errorf("reloaded host counted %d records, want %d", s2.number, total)
		}
		p3 := s2.join(t)
		defer p3.close()
		if p3.number != total {
			t.Fatalf("joiner to reloaded host told %d records, want %d", p3.number, total)
		}
		checkReplay(t, p3.rec.drain(t, int(total)), want)
	})
}

func isMarker(e any) bool {
	c, ok := e.(musical.Change)
	return ok && c.Entity.Number == 99
}

// checkReplay asserts a replayed record stream (catch-up or reload) is exactly
// the persisted sequence plus the trailing marker, with no previews or LookAts.
func checkReplay(t *testing.T, got []any, want []struct {
	row    mutation
	author musical.Author
}) {
	t.Helper()
	if len(got) != len(want)+1 {
		t.Fatalf("replayed %d records, want %d:\n%s", len(got), len(want)+1, describe(got))
	}
	for i, w := range want {
		if !w.row.is(got[i], w.author) {
			t.Errorf("replay %d: want %s by author %d, got %#v", i, w.row.name, w.author, got[i])
		}
	}
	if !isMarker(got[len(got)-1]) {
		t.Errorf("replay should end with the marker Change, got %#v", got[len(got)-1])
	}
	for _, e := range got {
		switch v := e.(type) {
		case musical.LookAt:
			t.Errorf("LookAt leaked into replay: %+v", v)
		case musical.Change:
			if !v.Commit {
				t.Errorf("preview Change leaked into replay: %+v", v)
			}
		case musical.Sculpt:
			if !v.Commit {
				t.Errorf("preview Sculpt leaked into replay: %+v", v)
			}
		case musical.Action:
			if !v.Commit {
				t.Errorf("preview Action leaked into replay: %+v", v)
			}
		}
	}
}

func describe(entries []any) string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = kindOf(e).String()
	}
	return strings.Join(names, ", ")
}

// --- recorder helpers -------------------------------------------------------

// expect waits for the row's record authored by `author` on the matching
// channel, skipping unrelated records (a replica also sees its own sends).
func (r *recorder) expect(t *testing.T, m mutation, author musical.Author, what string) {
	t.Helper()
	pred := func(e any) bool { return m.is(e, author) }
	switch m.kind {
	case kindImport:
		recvMatch(t, r.imports, gateTimeout, what, func(v musical.Import) bool { return pred(v) })
	case kindUpload:
		recvMatch(t, r.uploads, gateTimeout, what, func(v musical.Upload) bool { return pred(v) })
	case kindChange:
		recvMatch(t, r.changes, gateTimeout, what, func(v musical.Change) bool { return pred(v) })
	case kindAction:
		recvMatch(t, r.actions, gateTimeout, what, func(v musical.Action) bool { return pred(v) })
	case kindSculpt:
		recvMatch(t, r.sculpts, gateTimeout, what, func(v musical.Sculpt) bool { return pred(v) })
	case kindLookAt:
		recvMatch(t, r.lookAts, gateTimeout, what, func(v musical.LookAt) bool { return pred(v) })
	}
}

// drain waits until at least n records have arrived (in any kind), then
// waits a short grace period to catch stragglers, and returns everything seen
// in arrival order. Returning extras (not truncating to n) lets the caller
// detect over-delivery such as duplicated catch-up.
func (r *recorder) drain(t *testing.T, n int) []any {
	t.Helper()
	deadline := time.After(gateTimeout)
	for len(r.ordered()) < n {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d replayed records:\n%s", n, describe(r.ordered()))
		case <-time.After(10 * time.Millisecond):
		}
	}
	time.Sleep(100 * time.Millisecond)
	return r.ordered()
}

// --- harness: shared in-memory save -----------------------------------------

// sharedStorage is one in-memory .mus3 that every Open sees: the host appends
// committed records through its writer while catch-up readers and reloaded
// hosts read the same bytes from the top. This is what a real save file does.
type sharedStorage struct {
	mu  sync.Mutex
	buf []byte
}

func (s *sharedStorage) Open(musical.WorkID) (fs.File, error) { return &sharedFile{store: s}, nil }

// records returns the saved bytes after the format header.
func (s *sharedStorage) records() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buf) < len(musical.MagicHeader) {
		return nil
	}
	return append([]byte(nil), s.buf[len(musical.MagicHeader):]...)
}

type sharedFile struct {
	store *sharedStorage
	off   int
}

func (f *sharedFile) Read(p []byte) (int, error) {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	if f.off >= len(f.store.buf) {
		return 0, io.EOF
	}
	n := copy(p, f.store.buf[f.off:])
	f.off += n
	return n, nil
}

func (f *sharedFile) Write(p []byte) (int, error) {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	f.store.buf = append(f.store.buf, p...)
	return len(p), nil
}

func (f *sharedFile) Stat() (fs.FileInfo, error) {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	return memInfo{size: int64(len(f.store.buf))}, nil
}

func (f *sharedFile) Close() error { return nil }

// --- harness: session -------------------------------------------------------

type session struct {
	space   musical.UsersSpace3D
	host    *recorder
	number  uint64 // records the host loaded from the save
	clients chan musical.Networking
	done    chan struct{}
	once    sync.Once
	errs    *errSink
}

// startHost runs a musical host over the given save with a client feed the
// test can push late joiners into.
func startHost(t *testing.T, store musical.Storage, errs *errSink) *session {
	t.Helper()
	s := &session{host: newRecorder(), clients: make(chan musical.Networking), done: make(chan struct{}), errs: errs}
	feed := iter.Seq[musical.Networking](func(yield func(musical.Networking) bool) {
		for {
			select {
			case c := <-s.clients:
				if !yield(c) {
					return
				}
			case <-s.done:
				return
			}
		}
	})
	space, _, err := musical.Host("gate", feed, musical.WorkID{}, store, s.host, errs, hostAuthor)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	s.space = space
	m := recv(t, s.host.members, gateTimeout, "host self-assignment")
	if !m.Assign || m.Author != hostAuthor {
		t.Fatalf("host did not adopt its own author: %+v", m)
	}
	s.number = m.Number
	return s
}

func (s *session) stop() { s.once.Do(func() { close(s.done) }) }

type peer struct {
	space  musical.UsersSpace3D
	rec    *recorder
	author musical.Author
	number uint64 // records the host told us to catch up
	conns  []*memConn
}

func (s *session) join(t *testing.T) *peer {
	t.Helper()
	hostInstr, clientInstr := newPipe()
	hostMedia, clientMedia := newPipe()
	select {
	case s.clients <- musical.Networking{Instructions: hostInstr, MediaUploads: hostMedia, ErrorReports: s.errs}:
	case <-time.After(gateTimeout):
		t.Fatalf("host did not accept a new client")
	}
	rec := newRecorder()
	space, err := musical.Join(musical.Networking{Instructions: clientInstr, MediaUploads: clientMedia, ErrorReports: s.errs}, musical.WorkID{}, rec)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	m := recv(t, rec.members, gateTimeout, "joiner author assignment")
	if !m.Assign || m.Author == 0 || m.Author == hostAuthor {
		t.Fatalf("joiner was not assigned its own author: %+v", m)
	}
	return &peer{space: space, rec: rec, author: m.Author, number: m.Number, conns: []*memConn{hostInstr, clientInstr, hostMedia, clientMedia}}
}

func (p *peer) close() {
	for _, c := range p.conns {
		c.Close()
	}
}

func (e *errSink) contains(substr string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, err := range e.errs {
		if strings.Contains(err.Error(), substr) {
			return true
		}
	}
	return false
}
