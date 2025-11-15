package internal

import (
	"bytes"
	"fmt"
	"io"
	"os"
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
// resources from the community library .zip file that is hosted in the cloud.
//
// Once on startup, we load the central directory of the remote .zip file into memory,
// then whenever [Resource.Load] is called with a library path, we check if the local zip
// has this resource and if it doesn't, we fetch the corresponding resource from the remote
// .zip file using an HTTP range request and then we write this back into our local
// user://library.zip" and ask Godot to reload the .zip file.
//
// In Aviary, we may assume that CommunityResourceLoader is only ever called from a
// dedicated resource loading thread.
type CommunityResourceLoader struct {
	ResourceFormatLoader.Extension[CommunityResourceLoader]

	local map[string]pck.File
	cloud map[string]pck.File

	preview map[string]pck.File

	cache *httpseek.URL
}

func NewCommunityResourceLoader() *CommunityResourceLoader {
	crl := &CommunityResourceLoader{}
	crl.load()
	ProjectSettings.LoadResourcePack("user://library.pck", 0)
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
		cache, last_modified, err := httpseek.New("https://vpk.quetzal.community/library.pck")
		if err != nil {
			Engine.Raise(err)
			return
		}
		_ = last_modified // FIXME
		reader = cache
	}
	var cloud = crl.cloud[path]
	if strings.HasSuffix(path, ".import") || strings.HasSuffix(path, ".remap") {
		if _, err := reader.Seek(cloud.Seek, io.SeekStart); err != nil {
			Engine.Raise(err)
			return
		}
		var remap = make([]byte, cloud.Size)
		if _, err := io.ReadFull(reader, remap); err != nil {
			Engine.Raise(err)
			return
		}
		for line := range bytes.SplitSeq(remap, []byte("\n")) {
			if path, ok := bytes.CutPrefix(line, []byte("path=\"res://")); ok {
				crl.download(string(bytes.TrimSuffix(path, []byte("\""))))
				break
			}
		}
		reader = bytes.NewReader(remap)
		cloud.Seek = 0
	}
	local, err := os.OpenFile(OS.GetUserDataDir()+"/library.pck", os.O_RDWR, 0644)
	if err != nil {
		Engine.Raise(err)
		return
	}
	defer local.Close()
	if err := pck.Remap(local, reader, crl.local[path], cloud); err != nil {
		Engine.Raise(err)
		return
	}
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

func (crl *CommunityResourceLoader) load() {
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
	cloud, _, err := httpseek.New("https://vpk.quetzal.community/library.pck")
	if err != nil {
		Engine.Raise(err)
		return
	}
	defer cloud.Close()
	crl.cloud, err = pck.Index(cloud)
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
		if slot, ok := crl.local[path]; ok {
			if err := pck.Remap(local, preview, slot, entry); err != nil {
				Engine.Raise(err)
				return
			}
		}
	}
}
