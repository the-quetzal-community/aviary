package internal

import (
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

	lock sync.Mutex
	sync atomic.Bool

	reader io.Reader
	writer io.Writer
	closer func() error

	community signalling.API
}

var ShuttingDown = make(chan struct{})
var PendingSaves sync.WaitGroup

func OpenCloud(community signalling.API, work musical.WorkID) (fs.File, error) {
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
	}

	lazy := &lazyCloudReader{community: community, work: name, file: file, localSize: size}

	return &CloudBacked{
		name: name,
		// Bytes known up front = synthetic header + local part; cloud parts are
		// discovered lazily, so the loading bar fills on the local part and any
		// cloud catch-up (usually small / already cached) streams in after.
		size:   int64(len(musical.MagicHeader)) + size,
		writer: file,
		reader: io.MultiReader(strings.NewReader(musical.MagicHeader), file, lazy),
		closer: func() error {
			return file.Close()
		},
		community: community,
	}, nil
}

// lazyCloudReader defers the CloudParts network round-trip (and the per-part
// cloud readers it builds) until the decode first reads past the local part.
// io.MultiReader exhausts the local file before it, so by the time init runs the
// local mutations are already streaming through the main-thread replay queue and
// the round-trip overlaps that backlog. The per-part downloads stay lazy too
// (see cloudReader). Read on a single goroutine (the musical decode), no locking.
type lazyCloudReader struct {
	community signalling.API
	work      string
	file      *os.File
	localSize int64

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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	parts, err := l.community.CloudParts(ctx, signalling.WorkID(l.work))
	cancel()
	if err != nil {
		Engine.Raise(err) // not fatal: fall through with whatever parts we got.
	}
	var readers []io.Reader
	var other int
	for part, stat := range parts {
		if part == signalling.PartID(UserState.Device) {
			continue
		}
		other++
		readers = append(readers, &cloudReader{
			community: l.community,
			work:      signalling.WorkID(l.work),
			part:      part,
			size:      stat.Size,
			time:      stat.Time,
		})
	}
	// Local part empty but other devices have data: seed the local file with the
	// magic header so future saves append to a valid file. Safe here — the local
	// reader has already EOF'd (empty), and no save can happen until the load
	// finishes (well after this runs).
	if l.localSize == 0 && other > 0 {
		if _, werr := l.file.Write([]byte(musical.MagicHeader)); werr != nil {
			l.err = xray.New(werr)
			return
		}
	}
	l.r = io.MultiReader(readers...)
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
		stat, err := os.Stat(UserDataDir + "/" + string(cr.work) + "/" + string(cr.part) + ".mus3")
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
	if fw.sync.CompareAndSwap(false, true) {
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
