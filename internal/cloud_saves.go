package internal

import (
	"context"
	"encoding/base64"
	"io"
	"io/fs"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/OS"
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
	closer io.Closer

	community signalling.API
}

var ShuttingDown = make(chan struct{})
var PendingSaves sync.WaitGroup

func OpenCloud(community signalling.API, work musical.WorkID) (fs.File, error) {
	name := base64.RawURLEncoding.EncodeToString(work[:])

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	parts, err := community.CloudParts(ctx, signalling.WorkID(name))
	if err != nil {
		Engine.Raise(err) // not fatal.
	}

	if err := os.MkdirAll(OS.GetUserDataDir()+"/saves/"+name, 0777); err != nil {
		return nil, err
	}

	file, err := os.OpenFile(OS.GetUserDataDir()+"/saves/"+name+"/"+UserState.Device+".mus3", os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return nil, err
	}
	var size int64
	if stat, err := file.Stat(); err == nil {
		size = stat.Size()
	}
	var closers []io.Closer
	closers = append(closers, file)

	var readers []io.Reader
	readers = append(readers, file)

	for part, stat := range parts {
		if part == signalling.PartID(UserState.Device) {
			continue
		}
		readers = append(readers, &cloudReader{
			community: community,
			work:      signalling.WorkID(name),
			part:      part,
			size:      stat.Size,
			time:      stat.Time,
		})
		size += stat.Size
	}

	return &CloudBacked{
		name:      name,
		size:      size,
		writer:    file,
		reader:    io.MultiReader(readers...),
		closer:    file,
		community: community,
	}, nil
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
		stat, err := os.Stat(OS.GetUserDataDir() + "/" + string(cr.work) + "/" + string(cr.part) + ".mus3")
		if err != nil || stat.ModTime().Before(cr.time) || stat.Size() != cr.size {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			file, err := cr.community.LookupSave(ctx, cr.work, cr.part)
			if err != nil {
				return 0, err
			}
			cache, err := os.OpenFile(OS.GetUserDataDir()+"/"+string(cr.work)+"/"+string(cr.part)+".mus3", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
			if err != nil {
				return 0, err
			}
			cr.read = io.TeeReader(file, cache)
			cr.shut = func() {
				file.Close()
				cache.Close()
			}
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
		PendingSaves.Add(1)
		go func() {
			defer PendingSaves.Done()
			defer fw.sync.Store(false)
			select {
			case <-ShuttingDown:
			case <-time.After(10 * time.Minute):
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			fw.lock.Lock()
			defer fw.lock.Unlock()

			file, err := os.OpenFile(OS.GetUserDataDir()+"/saves/"+fw.name+"/"+UserState.Device+".mus3", os.O_RDONLY, 0666)
			if err != nil {
				Engine.Raise(err)
				return
			}
			if err := fw.community.InsertSave(ctx, signalling.WorkID(fw.name), signalling.PartID(UserState.Device), file); err != nil {
				Engine.Raise(err)
			}
		}()
	}
	fw.lock.Unlock()
	fw.size += int64(n)
	return n, err
}

func (cb *CloudBacked) Close() error {
	return cb.closer.Close()
}
