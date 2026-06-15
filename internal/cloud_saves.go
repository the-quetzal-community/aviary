package internal

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"graphics.gd/classdb/Engine"
	"runtime.link/api/xray"
	"the.quetzal.community/aviary/internal/ice/signalling"
	"the.quetzal.community/aviary/internal/musical"
)

type CloudBacked struct {
	name string
	size int64

	// cloud reports whether this work is being loaded inside a "together"
	// (cloud) session. When false the save is offline-only: we still load every
	// locally-cached part, but committed edits are not uploaded.
	cloud bool

	lock sync.Mutex
	sync atomic.Bool

	reader io.Reader
	writer io.Writer
	closer func() error

	community signalling.API
}

var ShuttingDown = make(chan struct{})
var PendingSaves sync.WaitGroup

// OpenCloud opens a work for loading. The current device's part is the writable
// part that future commits append to; EVERY other locally-cached part in the
// work folder is also read (parts authored on other devices, hand-copied parts,
// or parts the cloud no longer lists), so a save never loads empty just because
// its data lives in a part that doesn't match this device id. When cloud is true
// the cloud's part list is consulted as well (and committed edits upload); when
// false the save is offline-only and only local parts are read.
func OpenCloud(community signalling.API, work musical.WorkID, cloud bool) (fs.File, error) {
	name := base64.RawURLEncoding.EncodeToString(work[:])

	if err := os.MkdirAll(UserDataDir+"/saves/"+name, 0777); err != nil {
		return nil, err
	}

	file, err := os.OpenFile(UserDataDir+"/saves/"+name+"/"+UserState.Device+".mus3", os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return nil, err
	}
	var size int64
	if stat, err := file.Stat(); err == nil {
		size = stat.Size()
	}

	// The local device part decodes immediately; validate + skip its embedded
	// magic header here (local file, no network). The OTHER devices' parts — and
	// the CloudParts network round-trip needed to discover them — are resolved
	// lazily by lazyCloudReader, which io.MultiReader only reaches once the whole
	// local part has been read. By then the main thread has a deep replay
	// backlog, so the ~2s round-trip overlaps that work instead of blocking the
	// start of the load.
	if size > 0 {
		var header = make([]byte, len(musical.MagicHeader))
		if _, err := io.ReadFull(file, header); err != nil {
			file.Close()
			return nil, xray.New(err)
		}
		if string(header) != musical.MagicHeader {
			file.Close()
			return nil, xray.New(errors.New("invalid musical.Users3DScene file"))
		}
	} else {
		// Fresh local part: persist the magic header NOW so the on-disk file is a
		// valid musical.Users3DScene from its first byte. The reader below prepends
		// a *synthetic* header for the decoder, but the file itself must still
		// carry a real one — InsertSave uploads this raw file and peers validate
		// that header (cloudReader), and the next reload re-validates it (the
		// size > 0 branch above). Without this, a freshly created save is written
		// header-less, rejected as "invalid" on reload, which kills the musical
		// server goroutine (network.go server.run) and hard-freezes the next edit.
		// newStorage's own empty-file header write never fires here because
		// CloudBacked.Size() reports len(MagicHeader)+0, never 0.
		if _, err := file.Write([]byte(musical.MagicHeader)); err != nil {
			file.Close()
			return nil, xray.New(err)
		}
	}

	lazy := &lazyCloudReader{community: community, work: name, cloud: cloud}

	return &CloudBacked{
		name:  name,
		cloud: cloud,
		// Bytes known up front = synthetic header + local part; cloud parts are
		// discovered lazily, so the loading bar fills on the local part and any
		// cloud catch-up (usually small / already cached) streams in after.
		size:   int64(len(musical.MagicHeader)) + size,
		writer: file,
		// Buffer the local part: the decoder reads record-by-record via
		// binary.Read (tiny reads), so an unbuffered file turns ~65k records
		// into ~65k read syscalls. A 64K buffer collapses that to ~20 reads.
		// The buffer wraps only the READER side; the writer (line above) keeps
		// the raw *os.File, and saves only append AFTER the decode has read the
		// part to EOF (file offset = EOF), so buffered read-ahead can't corrupt
		// a later append. The bufio EOFs exactly at the local part's end, so the
		// MultiReader still advances to `lazy` only then — the cloud round-trip
		// stays deferred (unlike buffering the whole MultiReader, which could
		// read across the boundary and trigger the fetch during the load start).
		reader: io.MultiReader(strings.NewReader(musical.MagicHeader), bufio.NewReaderSize(file, decodeReadBuffer), lazy),
		closer: func() error {
			return file.Close()
		},
		community: community,
	}, nil
}

// decodeReadBuffer is the read-ahead buffer size wrapped around each save part
// (local device file + cloud parts) so the decoder's record-sized binary.Read
// calls are served from memory instead of hitting the file/network per record.
const decodeReadBuffer = 1 << 16 // 64 KiB

// lazyCloudReader defers the CloudParts network round-trip (and the per-part
// cloud readers it builds) until the decode first reads past the local part.
// io.MultiReader exhausts the local file before it, so by the time init runs the
// local mutations are already streaming through the main-thread replay queue and
// the round-trip overlaps that backlog. The per-part downloads stay lazy too
// (see cloudReader). Read on a single goroutine (the musical decode), no locking.
type lazyCloudReader struct {
	community signalling.API
	work      string
	cloud     bool // consult the cloud's part list (and download missing parts)

	once sync.Once
	r    io.Reader
	err  error
}

func (l *lazyCloudReader) Read(p []byte) (int, error) {
	l.once.Do(l.init)
	if l.err != nil {
		return 0, l.err
	}
	return l.r.Read(p)
}

func (l *lazyCloudReader) init() {
	// The writable current-device part is already in the outer MultiReader, so
	// skip it here; everything else (cloud-listed and/or locally cached) is
	// concatenated after it. Replay re-sorts every stroke into canonical
	// (Timing, Author) order, so the order parts appear in here is irrelevant.
	handled := map[string]bool{UserState.Device: true}
	var readers []io.Reader

	// Cloud-listed parts first (when in a together session): cloudReader serves
	// each from its local cache when fresh and only hits the network otherwise,
	// so peers' latest edits to an already-cached part still sync.
	if l.cloud {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		parts, err := l.community.CloudParts(ctx, signalling.WorkID(l.work))
		cancel()
		if err != nil {
			Engine.Raise(err) // not fatal: fall through to local parts.
		}
		for part, stat := range parts {
			if handled[string(part)] {
				continue
			}
			handled[string(part)] = true
			readers = append(readers, &cloudReader{
				community: l.community,
				work:      signalling.WorkID(l.work),
				part:      part,
				size:      stat.Size,
				time:      stat.Time,
			})
		}
	}

	// Then every OTHER locally-cached part the cloud didn't already account for:
	// parts authored on a now-retired device id, hand-copied parts, or — when
	// offline — all of them. This is what stops a save from loading empty when
	// its terrain lives in a part whose device id doesn't match this install.
	dir := UserDataDir + "/saves/" + l.work
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".mus3") {
				continue
			}
			part := strings.TrimSuffix(name, ".mus3")
			if handled[part] {
				continue
			}
			handled[part] = true
			r, err := localPartReader(dir + "/" + name)
			if err != nil {
				Engine.Raise(err) // skip a corrupt part rather than fail the whole load.
				continue
			}
			readers = append(readers, r)
		}
	} else {
		Engine.Raise(err)
	}

	// Buffer the concatenated parts: each underlying reader serves the decoder's
	// tiny per-record reads from a local file or a network stream; a 64K buffer
	// turns thousands of small reads (syscalls / round-trips) into a handful.
	// Every part reader strips its own magic header before yielding data, so the
	// buffer sees a clean post-header stream.
	l.r = bufio.NewReaderSize(io.MultiReader(readers...), decodeReadBuffer)
}

// localPartReader opens a locally-cached save part for reading, stripping its
// on-disk magic header so the body concatenates cleanly after the single header
// the decoder consumes up front. A header-only (empty) part contributes nothing.
// The file closes itself once the decoder reads it to EOF (see closeOnEOF).
func localPartReader(path string) (io.Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, xray.New(err)
	}
	var header [len(musical.MagicHeader)]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		f.Close()
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return strings.NewReader(""), nil // truncated/empty part: nothing to add.
		}
		return nil, xray.New(err)
	}
	if string(header[:]) != musical.MagicHeader {
		f.Close()
		return nil, xray.New(errors.New("invalid musical.Users3DScene file: " + path))
	}
	return &closeOnEOF{f: f}, nil
}

// closeOnEOF closes the underlying part file once it is exhausted, so the per-
// part handles opened for a multi-part load don't leak. The decoder reads each
// part to EOF during a normal load, which is what triggers the close.
type closeOnEOF struct{ f *os.File }

func (c *closeOnEOF) Read(p []byte) (int, error) {
	n, err := c.f.Read(p)
	if err != nil { // io.EOF or a real read error
		c.f.Close()
	}
	return n, err
}

type cloudReader struct {
	community signalling.API
	work      signalling.WorkID
	part      signalling.PartID
	time      time.Time
	size      int64
	open      bool
	read      io.Reader
	shut      func()
}

func (cr *cloudReader) Read(p []byte) (n int, err error) {
	if !cr.open {
		stat, err := os.Stat(UserDataDir + "/saves/" + string(cr.work) + "/" + string(cr.part) + ".mus3")
		if err != nil || stat.ModTime().Before(cr.time) || stat.Size() != cr.size {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			file, err := cr.community.LookupSave(ctx, cr.work, cr.part)
			if err != nil {
				cancel()
				return 0, err
			}
			cache, err := os.OpenFile(UserDataDir+"/saves/"+string(cr.work)+"/"+string(cr.part)+".mus3", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
			if err != nil {
				cancel()
				return 0, err
			}
			cr.read = io.TeeReader(file, cache)
			var header [len(musical.MagicHeader)]byte
			n, err := io.ReadFull(cr.read, header[:])
			if err != nil && !errors.Is(err, io.EOF) {
				cancel()
				return n, xray.New(err)
			} else if err == nil {
				if string(header[:]) != musical.MagicHeader {
					cancel()
					return n, xray.New(errors.New("invalid musical.Users3DScene file"))
				}
			}
			cr.shut = func() {
				file.Close()
				cache.Close()
				cancel()
			}
			cr.open = true
		} else {
			local, err := os.OpenFile(UserDataDir+"/saves/"+string(cr.work)+"/"+string(cr.part)+".mus3", os.O_RDONLY, 0666)
			if err != nil {
				return 0, err
			}
			cr.read = local
			cr.open = true
		}
	}
	n, err = cr.read.Read(p)
	if err == io.EOF {
		cr.shut()
	}
	return n, err
}

func (fw *CloudBacked) Stat() (fs.FileInfo, error) {
	return fw, nil
}

func (fw *CloudBacked) Name() string { return fw.name }

func (fw *CloudBacked) Size() int64 {
	return fw.size
}

func (fw *CloudBacked) Mode() fs.FileMode {
	return 0666
}

func (fw *CloudBacked) IsDir() bool {
	return false
}

func (fw *CloudBacked) Sys() any { return nil }

func (fw *CloudBacked) ModTime() (t time.Time) {
	return time.Now()
}

func (fw *CloudBacked) Read(p []byte) (n int, err error) {
	return fw.reader.Read(p)
}

func (fw *CloudBacked) Write(p []byte) (n int, err error) {
	fw.lock.Lock()
	n, err = fw.writer.Write(p)
	if fw.cloud && fw.sync.CompareAndSwap(false, true) {
		savePath := UserDataDir + "/saves/" + fw.name + "/" + UserState.Device + ".mus3"
		device := UserState.Device
		PendingSaves.Go(func() {
			defer fw.sync.Store(false)
			var shuttingDown bool
			select {
			case <-ShuttingDown:
				shuttingDown = true
			case <-time.After(10 * time.Minute):
			}
			raise := func(err error) {
				if shuttingDown {
					log.Println("aviary: cloud save error during shutdown:", err)
					return
				}
				Engine.Raise(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			fw.lock.Lock()
			defer fw.lock.Unlock()

			file, err := os.OpenFile(savePath, os.O_RDONLY, 0666)
			if err != nil {
				raise(err)
				return
			}
			if stat, err := file.Stat(); err == nil && stat.Size() < int64(len(musical.MagicHeader)) {
				return
			}
			if err := fw.community.InsertSave(ctx, signalling.WorkID(fw.name), signalling.PartID(device), file); err != nil {
				raise(err)
			}
		})
	}
	fw.lock.Unlock()
	fw.size += int64(n)
	return n, err
}

func (cb *CloudBacked) Close() error {
	return cb.closer()
}
