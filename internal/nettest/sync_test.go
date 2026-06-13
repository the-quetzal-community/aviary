// Package nettest holds headless end-to-end tests for the realtime layer: a
// host and a joining client exchanging a small musical scene. Two tests share
// one harness:
//
//   - TestMusicalSyncHeadless wires musical.Host <-> musical.Join over in-memory
//     pipes (no WebRTC, no network). It pins the sync contract — a joiner is
//     assigned an author, the host's contributions reach it, and its own
//     contributions reach the host — and runs everywhere, fast.
//
//   - TestMusicalSyncIntegration runs the same checks over the real
//     networking.Connectivity transport against the live Quetzal Community
//     signalling server, using the developer's stored credentials. It is gated
//     behind AVIARY_INTEGRATION=1 (and -short skips it) so ordinary test runs
//     never touch the network. This is the one that reproduces a real join:
//     if it connects and syncs, a join failure in the app is environmental
//     (TURN/ICE) rather than a logic regression; if it hangs/fails here, the
//     fault is in the transport.
package nettest

import (
	"encoding/json"
	"io"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"the.quetzal.community/aviary/internal/musical"
	"the.quetzal.community/aviary/internal/networking"
)

// hostAuthor is the author the host adopts for its own contributions in these
// tests; joiners are assigned 1..255, so this stays clear of them.
const hostAuthor musical.Author = 200

func TestMusicalSyncHeadless(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	var errs errSink

	// One bidirectional pipe per channel the musical layer uses. The host's
	// view of the client and the client share opposite ends.
	hostInstr, clientInstr := newPipe()
	hostMedia, clientMedia := newPipe()
	defer hostInstr.Close()
	defer clientInstr.Close()
	defer hostMedia.Close()
	defer clientMedia.Close()

	hostSideClient := musical.Networking{Instructions: hostInstr, MediaUploads: hostMedia, ErrorReports: &errs}
	clientSide := musical.Networking{Instructions: clientInstr, MediaUploads: clientMedia, ErrorReports: &errs}

	// Yield exactly one client to the host, then block so the host's clients
	// stream stays open (closing it would shut the server down mid-test).
	clients := iter.Seq[musical.Networking](func(yield func(musical.Networking) bool) {
		if !yield(hostSideClient) {
			return
		}
		<-stop
	})

	host := newRecorder()
	client := newRecorder()

	hostSpace, _, err := musical.Host("headless-test", clients, musical.WorkID{}, memStorage{}, host, &errs, hostAuthor)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	clientSpace, err := musical.Join(clientSide, musical.WorkID{}, client)
	if err != nil {
		t.Fatalf("join: %v", err)
	}

	runSyncChecks(t, hostSpace, clientSpace, host, client, 5*time.Second)
}

func TestMusicalSyncIntegration(t *testing.T) {
	if os.Getenv("AVIARY_INTEGRATION") == "" {
		t.Skip("set AVIARY_INTEGRATION=1 to run the live signalling integration test")
	}
	if testing.Short() {
		t.Skip("integration test hits the live signalling server; skipped in -short")
	}
	secret := resolveSecret(t)

	stop := make(chan struct{})
	defer close(stop)
	var errs errSink
	printer := func(format string, args ...any) { t.Logf("net: "+format, args...) }
	raiser := func(err error) { errs.ReportError(err) }

	hostConn := &networking.Connectivity{Authentication: secret, Print: printer, Raise: raiser}
	joinConn := &networking.Connectivity{Authentication: secret, Print: printer, Raise: raiser}

	// The host's per-peer callback fires once the data channel opens; wrap each
	// peer as a musical client and feed it to the server's clients stream. This
	// mirrors how internal/client.go bridges networking -> musical.
	clients := make(chan musical.Networking, 1)
	code, err := hostConn.Host(make(chan []byte, 16), func(peer networking.Client) {
		clients <- musical.Networking{
			Instructions: forPeer{peer},
			MediaUploads: blockConn{stop},
			ErrorReports: &errs,
		}
	})
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	t.Logf("hosting room %s", code)

	seq := iter.Seq[musical.Networking](func(yield func(musical.Networking) bool) {
		for {
			select {
			case c := <-clients:
				if !yield(c) {
					return
				}
			case <-stop:
				return
			}
		}
	})

	host := newRecorder()
	client := newRecorder()

	hostSpace, _, err := musical.Host("integration-test", seq, musical.WorkID{}, memStorage{}, host, &errs, hostAuthor)
	if err != nil {
		t.Fatalf("musical host: %v", err)
	}

	// Join blocks until the data channel opens (or the peer connection fails).
	joinUpdates := make(chan []byte, 64)
	if err := joinConn.Join(networking.Code(code), joinUpdates); err != nil {
		t.Fatalf("join %s: %v", code, err)
	}
	clientSpace, err := musical.Join(musical.Networking{
		Instructions: viaPeer{net: joinConn, updates: joinUpdates},
		MediaUploads: blockConn{stop},
		ErrorReports: &errs,
	}, musical.WorkID{}, client)
	if err != nil {
		t.Fatalf("musical join: %v", err)
	}

	// The WebRTC handshake and STUN/TURN gathering add real latency, so allow
	// well past the offer timeout before declaring a hang.
	runSyncChecks(t, hostSpace, clientSpace, host, client, 35*time.Second)
}

// runSyncChecks drives the shared scenario: the client is assigned an author,
// a design the host imports and places reaches the client, and a change the
// client makes (as its assigned author) reaches the host.
func runSyncChecks(t *testing.T, hostSpace, clientSpace musical.UsersSpace3D, host, client *recorder, timeout time.Duration) {
	t.Helper()

	m := recv(t, client.members, timeout, "client author assignment")
	if !m.Assign || m.Author == 0 {
		t.Fatalf("client was not assigned a joiner author: %+v", m)
	}
	clientAuthor := m.Author
	t.Logf("client assigned author %d", clientAuthor)

	const uri = "res://library/everything/avatar/bald_eagle.glb"
	design := musical.Design{Author: hostAuthor, Number: 1}
	if err := hostSpace.Import(musical.Import{Design: design, Import: uri}); err != nil {
		t.Fatalf("host import: %v", err)
	}
	if err := hostSpace.Change(musical.Change{
		Author: hostAuthor,
		Entity: musical.Entity{Author: hostAuthor, Number: 1},
		Design: design,
		Commit: true,
	}); err != nil {
		t.Fatalf("host change: %v", err)
	}
	if got := recv(t, client.imports, timeout, "client receives Import"); got.Import != uri {
		t.Errorf("client Import URI = %q, want %q", got.Import, uri)
	}
	if got := recv(t, client.changes, timeout, "client receives Change"); got.Design != design {
		t.Errorf("client Change design = %+v, want %+v", got.Design, design)
	}

	clientDesign := musical.Design{Author: clientAuthor, Number: 1}
	if err := clientSpace.Change(musical.Change{
		Author: clientAuthor,
		Entity: musical.Entity{Author: clientAuthor, Number: 1},
		Design: clientDesign,
		Commit: true,
	}); err != nil {
		t.Fatalf("client change: %v", err)
	}
	// The host replica also records the host's own Change (author hostAuthor)
	// from above, so skip past anything that isn't the client's.
	got := recvMatch(t, host.changes, timeout, "host receives client Change",
		func(c musical.Change) bool { return c.Author == clientAuthor })
	if got.Design != clientDesign {
		t.Errorf("host saw client Change design %+v, want %+v", got.Design, clientDesign)
	}
}

// --- credentials -----------------------------------------------------------

// resolveSecret loads the developer's account secret: AVIARY_SECRET if set,
// otherwise the Secret field of the Godot config's user.json (path overridable
// via AVIARY_USER_JSON). Skips the test when no credential is available.
func resolveSecret(t *testing.T) string {
	t.Helper()
	if s := os.Getenv("AVIARY_SECRET"); s != "" {
		return s
	}
	path := os.Getenv("AVIARY_USER_JSON")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("no credentials: set AVIARY_SECRET (home dir lookup failed: %v)", err)
		}
		path = filepath.Join(home, ".config", "user.json")
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no credentials: set AVIARY_SECRET or provide %s (%v)", path, err)
	}
	var state struct{ Secret string }
	if err := json.Unmarshal(buf, &state); err != nil || state.Secret == "" {
		t.Skipf("no usable Secret in %s", path)
	}
	return state.Secret
}

// --- harness: recorder replica ---------------------------------------------

// recorder is a musical.UsersSpace3D that forwards every observed instruction
// onto a channel so a test can assert what a replica received. Channels are
// buffered so the (synchronous) server/client apply loops never block on a
// slow assertion.
type recorder struct {
	members chan musical.Member
	uploads chan musical.Upload
	sculpts chan musical.Sculpt
	imports chan musical.Import
	changes chan musical.Change
	actions chan musical.Action
	lookAts chan musical.LookAt
}

func newRecorder() *recorder {
	return &recorder{
		members: make(chan musical.Member, 64),
		uploads: make(chan musical.Upload, 64),
		sculpts: make(chan musical.Sculpt, 64),
		imports: make(chan musical.Import, 64),
		changes: make(chan musical.Change, 64),
		actions: make(chan musical.Action, 64),
		lookAts: make(chan musical.LookAt, 64),
	}
}

func (r *recorder) Member(v musical.Member) error { send(r.members, v); return nil }
func (r *recorder) Upload(v musical.Upload) error { send(r.uploads, v); return nil }
func (r *recorder) Sculpt(v musical.Sculpt) error { send(r.sculpts, v); return nil }
func (r *recorder) Import(v musical.Import) error { send(r.imports, v); return nil }
func (r *recorder) Change(v musical.Change) error { send(r.changes, v); return nil }
func (r *recorder) Action(v musical.Action) error { send(r.actions, v); return nil }
func (r *recorder) LookAt(v musical.LookAt) error { send(r.lookAts, v); return nil }

// send never blocks the caller (the apply loop): if a buffer fills because a
// test isn't draining that instruction type, the extra is dropped rather than
// wedging the server.
func send[T any](ch chan T, v T) {
	select {
	case ch <- v:
	default:
	}
}

func recv[T any](t *testing.T, ch <-chan T, timeout time.Duration, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(timeout):
		t.Fatalf("timed out after %s waiting for %s", timeout, what)
		panic("unreachable")
	}
}

// recvMatch reads from ch until pred is satisfied (skipping non-matching
// values) or the timeout elapses. Used where a replica also records the
// observer's own contributions, which must be skipped.
func recvMatch[T any](t *testing.T, ch <-chan T, timeout time.Duration, what string, pred func(T) bool) T {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case v := <-ch:
			if pred(v) {
				return v
			}
		case <-deadline:
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
			panic("unreachable")
		}
	}
}

// --- harness: error sink ---------------------------------------------------

// errSink collects errors without touching *testing.T, so background goroutines
// reporting an error after the test returns can't trigger a "log after test
// completed" panic.
type errSink struct {
	mu   sync.Mutex
	errs []error
}

func (e *errSink) ReportError(err error) {
	e.mu.Lock()
	e.errs = append(e.errs, err)
	e.mu.Unlock()
}

// --- harness: in-memory storage --------------------------------------------

// memStorage hands out a fresh empty in-memory .mus3 per Open. A joiner that
// connects before any committed contribution catches up zero instructions, so
// independent empty files are sufficient for these live-sync tests.
type memStorage struct{}

func (memStorage) Open(musical.WorkID) (fs.File, error) { return &memFile{}, nil }

// memFile is a read/write/grow in-memory file implementing io/fs.File (plus
// io.Writer, which the musical storage uses to append committed instructions).
type memFile struct {
	mu  sync.Mutex
	buf []byte
	off int
}

func (f *memFile) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.off >= len(f.buf) {
		return 0, io.EOF
	}
	n := copy(p, f.buf[f.off:])
	f.off += n
	return n, nil
}

func (f *memFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buf = append(f.buf, p...)
	return len(p), nil
}

func (f *memFile) Stat() (fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return memInfo{size: int64(len(f.buf))}, nil
}

func (f *memFile) Close() error { return nil }

// memInfo is the minimal fs.FileInfo the musical storage reads (only Size).
type memInfo struct{ size int64 }

func (i memInfo) Name() string       { return "scene.mus3" }
func (i memInfo) Size() int64        { return i.size }
func (i memInfo) Mode() fs.FileMode  { return 0 }
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return false }
func (i memInfo) Sys() any           { return nil }

// --- harness: in-memory pipe (headless transport) --------------------------

// memConn is one end of an in-memory, bidirectional byte-message pipe
// implementing musical.Connection. Send copies its payload (the musical encoder
// may reuse buffers) and hands it to the peer's receive queue.
type memConn struct {
	in     <-chan []byte
	out    chan<- []byte
	closed chan struct{}
	close  func()
}

func (m *memConn) Send(b []byte) error {
	cp := append([]byte(nil), b...)
	select {
	case m.out <- cp:
		return nil
	case <-m.closed:
		return io.ErrClosedPipe
	}
}

func (m *memConn) Recv() ([]byte, error) {
	select {
	case b := <-m.in:
		return b, nil
	case <-m.closed:
		return nil, io.EOF
	}
}

func (m *memConn) Close() error { m.close(); return nil }

func newPipe() (*memConn, *memConn) {
	ab := make(chan []byte, 64)
	ba := make(chan []byte, 64)
	closed := make(chan struct{})
	var once sync.Once
	closer := func() { once.Do(func() { close(closed) }) }
	a := &memConn{in: ba, out: ab, closed: closed, close: closer}
	b := &memConn{in: ab, out: ba, closed: closed, close: closer}
	return a, b
}

// --- harness: networking.Connectivity adapters (integration transport) -----

// forPeer adapts a host-side networking.Client into a musical.Connection. It
// mirrors internal.networkingFor.
type forPeer struct{ peer networking.Client }

func (f forPeer) Send(b []byte) error {
	select {
	case f.peer.Send <- b:
		return nil
	case <-f.peer.Done:
		return io.EOF
	}
}

func (f forPeer) Recv() ([]byte, error) {
	select {
	case b := <-f.peer.Recv:
		return b, nil
	case <-f.peer.Done:
		return nil, io.EOF
	}
}

func (f forPeer) Close() error { return nil }

// viaPeer adapts the joining side (a *networking.Connectivity plus its updates
// channel) into a musical.Connection. It mirrors internal.networkingVia.
type viaPeer struct {
	net     *networking.Connectivity
	updates chan []byte
}

func (v viaPeer) Send(b []byte) error { v.net.Send(b); return nil }

func (v viaPeer) Recv() ([]byte, error) {
	select {
	case b, ok := <-v.updates:
		if !ok {
			return nil, io.EOF
		}
		return b, nil
	case <-v.net.Done():
		return nil, io.EOF
	}
}

func (v viaPeer) Close() error { return nil }

// blockConn is a media-uploads stand-in: these tests never upload files, so it
// noops Send and blocks Recv until the test ends (mirrors internal.stubbedNetwork
// but releases its goroutine on teardown).
type blockConn struct{ done chan struct{} }

func (b blockConn) Send([]byte) error     { return nil }
func (b blockConn) Recv() ([]byte, error) { <-b.done; return nil, io.EOF }
func (b blockConn) Close() error          { return nil }
