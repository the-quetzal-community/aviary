package internal

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"strings"
	"time"

	"graphics.gd/classdb/Engine"
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
// CommunityResourceLoader is only ever called from a single dedicated
// resource loading thread. That invariant is enforced by routing every
// aviary load and existence check through the loader goroutine in
// resource_thread.go (LoadSync / LoadAsync / ExistsSync) — which is what
// makes the lock-free maps below safe. Do not call Resource.Load or
// ResourceLoader.Exists directly from aviary code; use those helpers.
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
func (crl *CommunityResourceLoader) RecognizePath(requested string, atype string) bool {
	// Normalize paths that may contain ".." (e.g. relative material references
	// baked into MaterialSharingMeshInstance3D.Material strings inside library
	// foliage/mineral/etc props, which can be of the form
	// "res://library/author/foliage/../texture/hash.tres").
	// The maps from pck.Index use canonical paths, so we must use the cleaned
	// form for map lookups and to trigger downloads. Godot itself normalizes
	// during actual resource resolution, but our on-demand logic must too.
	clean := path.Clean(strings.TrimPrefix(requested, "res://"))
	path_import := clean + ".import"
	path_remap := clean + ".remap"
	if entry, ok := crl.local[clean]; ok && !entry.Missing() {
		if cloud, ok := crl.cloud[clean]; ok {
			if entry.Hash != cloud.Hash && cloud.Size <= entry.Size {
				crl.download(clean)
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
	if _, ok := crl.cloud[clean]; ok {
		crl.download(clean)
		return false
	}
	return false
}

func (crl *CommunityResourceLoader) remap(entry pck.File) bool {
	local, err := os.OpenFile(UserDataDir+"/preview.pck", os.O_RDWR, 0644)
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
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if crl.cache == nil {
			cache, err := httpseek.New("https://vpk.quetzal.community/library.pck")
			if err != nil {
				Engine.Raise(err)
				return
			}
			crl.cache = cache
		}
		reader := crl.cache
		local, err := os.OpenFile(UserDataDir+"/library.pck", os.O_RDWR, 0644)
		if err != nil {
			Engine.Raise(err)
			return
		}
		// Note: defer close is per-attempt; we close explicitly on retry.
		func() {
			defer local.Close()
			next := crl.local[path]
			prev := crl.cloud[path]
			if err := pck.Remap(local, reader, next, prev); err != nil {
				lastErr = err
				// Transient network error (e.g. "http2: response body closed").
				// Invalidate any partial bytes written to the reserved data slot
				// in the pck. This prevents Godot from later reading corrupt
				// .scn / .ctex data (leading to BasisUniversal unpack failures,
				// OOB in cowdata, and hard crashes like illegal instruction).
				// Zeroing ensures a clean "empty resource" parse failure instead.
				if next.Size > 0 {
					if _, seekErr := local.Seek(next.Seek, io.SeekStart); seekErr == nil {
						zero := make([]byte, 64<<10)
						rem := next.Size
						for rem > 0 {
							n := int64(len(zero))
							if n > rem {
								n = rem
							}
							local.Write(zero[:n])
							rem -= n
						}
					}
				}
				// Re-mark missing on disk (defensive, in case dir was touched).
				if next.Head > 0 {
					next.SetMissing(true, local)
				}
				// Force a fresh connection on next attempt.
				if crl.cache != nil {
					crl.cache.Close()
					crl.cache = nil
				}
				if attempt < maxAttempts-1 {
					// small backoff
					time.Sleep(time.Duration(1<<uint(attempt)) * 250 * time.Millisecond)
				}
				return
			}
			// Success: clear missing flag in memory and on disk.
			file := crl.local[path]
			file.Flag = 0
			crl.local[path] = file
			if file.Head > 0 {
				file.SetMissing(false, local)
			}
			lastErr = nil
		}()
		if lastErr == nil {
			return
		}
	}
	if lastErr != nil {
		Engine.Raise(fmt.Errorf("failed to download resource %q from community library: %v", path, lastErr))
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

func (crl *CommunityResourceLoader) load(resource *httpseek.URL) {
	local, err := os.OpenFile(UserDataDir+"/library.pck", os.O_RDWR|os.O_CREATE, 0644)
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
	preview, err := os.OpenFile(UserDataDir+"/preview.pck", os.O_RDWR|os.O_CREATE, 0644)
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
		// These are engine-managed metadata that Godot regenerates per
		// export, so they legitimately differ between the two pcks. The
		// rest of `.godot/` — notably `.godot/imported/*.ctex` — holds
		// imported resource data (compressed textures, etc.) that must
		// be remapped or Godot will fail to load referenced resources.
		switch path {
		case ".godot/uid_cache.bin",
			".godot/global_script_class_cache.cfg",
			"project.binary":
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
