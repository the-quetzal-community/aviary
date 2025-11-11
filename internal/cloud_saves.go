package internal

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/OS"
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	parts, err := community.CloudParts(ctx, signalling.WorkID(name))
	if err != nil {
		Engine.Raise(err) // not fatal.
	}
	cancel()

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)

	if err := os.MkdirAll(OS.GetUserDataDir()+"/saves/"+name, 0777); err != nil {
		cancel()
		return nil, err
	}

	file, err := os.OpenFile(OS.GetUserDataDir()+"/saves/"+name+"/"+UserState.Device+".mus3", os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		cancel()
		return nil, err
	}
	var size int64
	var total_size int64
	if stat, err := file.Stat(); err == nil {
		size = stat.Size()
		total_size += stat.Size()
	}

	var readers []io.Reader
	readers = append(readers, strings.NewReader(musical.MagicHeader))
	readers = append(readers, file)

	var other_parts int
	for part, stat := range parts {
		if part == signalling.PartID(UserState.Device) {
			continue
		}
		other_parts++
		readers = append(readers, &cloudReader{
			community: community,
			work:      signalling.WorkID(name),
			part:      part,
			size:      stat.Size,
			time:      stat.Time,
		})
		total_size += stat.Size
	}

	if size == 0 && other_parts > 0 {
		if _, err := file.Write([]byte(musical.MagicHeader)); err != nil {
			cancel()
			return nil, xray.New(err)
		}
		total_size++
	} else if size > 0 {
		var header = make([]byte, len(musical.MagicHeader))
		if _, err := io.ReadFull(file, header); err != nil {
			cancel()
			return nil, xray.New(err)
		}
		if string(header) != musical.MagicHeader {
			cancel()
			return nil, xray.New(errors.New("invalid musical.Users3DScene file"))
		}
	}

	return &CloudBacked{
		name:   name,
		size:   total_size,
		writer: file,
		reader: io.MultiReader(readers...),
		closer: func() error {
			cancel()
			if err := file.Close(); err != nil {
				return err
			}
			return nil
		},
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
			file, err := cr.community.LookupSave(ctx, cr.work, cr.part)
			if err != nil {
				cancel()
				return 0, err
			}
			cache, err := os.OpenFile(OS.GetUserDataDir()+"/saves/"+string(cr.work)+"/"+string(cr.part)+".mus3", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
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
			local, err := os.OpenFile(OS.GetUserDataDir()+"/saves/"+string(cr.work)+"/"+string(cr.part)+".mus3", os.O_RDONLY, 0666)
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
			if stat, err := file.Stat(); err == nil && stat.Size() < int64(len(musical.MagicHeader)) {
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
	return cb.closer()
}
