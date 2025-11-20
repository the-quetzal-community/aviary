package internal

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/ProjectSettings"
	"graphics.gd/classdb/ResourceFormatLoader"
	"the.quetzal.community/aviary/internal/httpseek"
	"the.quetzal.community/aviary/internal/pck"
)

// CommunityResourceLoader is responsible for loading community library resources into
// Godot's "res://" resource file system. This is achieved by pulling individual
// resources from the community library .pck file that is hosted in the cloud.
//
// Once on startup, we load the file directory of the remote .pck, we write this locally,
// along with preallocated space for each file, then whenever [Resource.Load] is called with
// a library path, we check if the local pck has this resource and if it doesn't, we fetch the
// corresponding resource from the remote .pck file using an HTTP range request and then we
// write this back into our local user://library.pck" before Godot has the chance to read it.
//
// In Aviary, we may assume that CommunityResourceLoader is only ever called from a
// single dedicated resource loading thread.
type CommunityResourceLoader struct {
	ResourceFormatLoader.Extension[CommunityResourceLoader]

	local map[string]pck.File
	cloud map[string]pck.File

	preview map[string]pck.File

	cache *httpseek.URL
}

func NewCommunityResourceLoader() *CommunityResourceLoader {
	crl := &CommunityResourceLoader{}
	if runtime.GOOS == "js" {
		return crl
	}
	defer ProjectSettings.LoadResourcePack("user://library.pck", 0)
	if os.Getenv("AVIARY_DOWNLOAD") == "0" {
		crl.load(nil)
		return crl
	}
	cloud, err := httpseek.New("https://vpk.quetzal.community/library.pck")
	if err != nil {
		Engine.Raise(err)
	} else {
		cloud.OnResourceModified(crl.load)
	}
	crl.load(cloud)
	return crl
}

// Tells whether or not this loader should load a resource from its resource path for a given type.
//
// If it is not implemented, the default behavior returns whether the path's extension is within the ones provided by [GetRecognizedExtensions], and if the type is within the ones provided by [GetResourceType].
//
// [GetRecognizedExtensions]: https://pkg.go.dev/graphics.gd/classdb/ResourceFormatLoader#Interface
// [GetResourceType]: https://pkg.go.dev/graphics.gd/classdb/ResourceFormatLoader#Interface
func (crl *CommunityResourceLoader) RecognizePath(path string, atype string) bool {
	path = strings.TrimPrefix(path, "res://")
	path_import := path + ".import"
	path_remap := path + ".remap"
	if entry, ok := crl.local[path]; ok && !entry.Missing() {
		if cloud, ok := crl.cloud[path]; ok {
			if entry.Hash != cloud.Hash && cloud.Size <= entry.Size {
				crl.download(path)
			}
		}
		return false
	}
	if entry, ok := crl.preview[path_import]; ok {
		return crl.remap(entry)
	}
	if entry, ok := crl.preview[path_remap]; ok {
		return crl.remap(entry)
	}
	if _, ok := crl.cloud[path]; ok {
		crl.download(path)
		return false
	}
	return false
}

func (crl *CommunityResourceLoader) remap(entry pck.File) bool {
	local, err := os.OpenFile(OS.GetUserDataDir()+"/preview.pck", os.O_RDWR, 0644)
	if err != nil {
		Engine.Raise(err)
		return false
	}
	defer local.Close()
	header, err := entry.Bytes(local)
	if err != nil {
		Engine.Raise(err)
		return false
	}
	for line := range bytes.SplitSeq(header, []byte("\n")) {
		if path, ok := bytes.CutPrefix(line, []byte("path=\"res://")); ok {
			remapped := string(bytes.TrimSuffix(path, []byte("\"")))
			return crl.RecognizePath(remapped, "")
		}
	}
	return false
}

func (crl *CommunityResourceLoader) download(path string) {
	var reader io.ReadSeeker = crl.cache
	if crl.cache == nil {
		cache, err := httpseek.New("https://vpk.quetzal.community/library.pck")
		if err != nil {
			Engine.Raise(err)
			return
		}
		reader = cache
	}
	local, err := os.OpenFile(OS.GetUserDataDir()+"/library.pck", os.O_RDWR, 0644)
	if err != nil {
		Engine.Raise(err)
		return
	}
	defer local.Close()
	if err := pck.Remap(local, reader, crl.local[path], crl.cloud[path]); err != nil {
		Engine.Raise(fmt.Errorf("failed to download resource %q from community library: %v", path, err))
		return
	}
	file := crl.local[path]
	file.Flag = 0
	crl.local[path] = file
}

type localFetcher struct {
	*os.File
}

func (f localFetcher) Fetch(start, end *int64) (io.Reader, error) {
	var reader io.Reader
	switch {
	case start != nil && end != nil:
		if _, err := f.Seek(*start, io.SeekStart); err != nil {
			return nil, err
		}
		reader = io.LimitReader(f, *end-*start+1)
	case start == nil && end != nil:
		if _, err := f.Seek(-*end, io.SeekEnd); err != nil {
			return nil, err
		}
		reader = f
	case start != nil && end == nil:
		if _, err := f.Seek(*start, io.SeekStart); err != nil {
			return nil, err
		}
		reader = f
	}
	return reader, nil
}

func (crl *CommunityResourceLoader) load(resource *httpseek.URL) {
	local, err := os.OpenFile(OS.GetUserDataDir()+"/library.pck", os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		Engine.Raise(err)
		return
	}
	defer local.Close()
	stat, err := local.Stat()
	if err != nil {
		Engine.Raise(err)
		return
	}
	if stat.Size() == 0 {
		if err := pck.Create(local); err != nil {
			Engine.Raise(err)
			return
		}
	}
	if resource != nil {
		crl.cloud, err = pck.Index(resource)
		if err != nil {
			Engine.Raise(err)
			return
		}
		if _, err := local.Seek(0, io.SeekStart); err != nil {
			Engine.Raise(err)
			return
		}
		if err := pck.Append(local, crl.cloud); err != nil {
			Engine.Raise(fmt.Errorf("failed to update local library.pck: %w", err))
			return
		}
		if _, err := local.Seek(0, io.SeekStart); err != nil {
			Engine.Raise(err)
			return
		}
		crl.local, err = pck.Index(local)
		if err != nil {
			Engine.Raise(err)
			return
		}
	}
	preview, err := os.OpenFile(OS.GetUserDataDir()+"/preview.pck", os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		Engine.Raise(err)
		return
	}
	defer preview.Close()
	crl.preview, err = pck.Index(preview)
	if err != nil {
		Engine.Raise(err)
		return
	}
	if _, err := local.Seek(0, io.SeekStart); err != nil {
		Engine.Raise(err)
		return
	}
	for path, entry := range crl.preview {
		if path == ".godot/uid_cache.bin" {
			continue
		}
		if slot, ok := crl.local[path]; ok {
			if err := pck.Remap(local, preview, slot, entry); err != nil {
				Engine.Raise(fmt.Errorf("failed to update local of %s from preview.pck: %w", path, err))
				return
			}
		}
	}
}
