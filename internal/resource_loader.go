package internal

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"time"

	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/ProjectSettings"
	"graphics.gd/classdb/ResourceFormatLoader"
	"the.quetzal.community/aviary/internal/httpseek"
	"the.quetzal.community/aviary/internal/pck"
)

// libraryURL is the cloud-hosted community library .pck streamed via HTTP
// range requests (see [httpseek] and [CommunityResourceLoader]).
const libraryURL = "https://vpk.quetzal.community/library.pck"

// previewURL is the cloud-hosted metadata .pck (the .import/.remap/.ctex/.region
// tier) that is downloaded in full and mounted at startup; see LibraryDownloader.
const previewURL = "https://vpk.quetzal.community/preview.pck"

// refreshSidecarName is the file under UserDataDir that records a completed
// background refresh waiting to be promoted into the local library.pck on the
// next launch. Its mere existence means the refresh finished in full.
const refreshSidecarName = "library.pck.refresh"

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

	// refreshMu serializes background refreshes so that, even if load() runs
	// more than once, only one appends to the local pck at a time and each
	// computes its append offset from the current end-of-file.
	refreshMu sync.Mutex
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
	cloud, err := httpseek.New(libraryURL)
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
		// We already hold usable bytes for this file, so serve them without
		// blocking. If the cloud copy changed, the background refresh started
		// in load() re-downloads it and the next launch promotes the fresh
		// bytes — the mount never blocks on a network download for a file we
		// can already serve (stale-while-revalidate).
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
			cache, err := httpseek.New(libraryURL)
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
	var stale []refreshItem
	if resource != nil {
		crl.cloud, err = pck.Index(resource)
		if err != nil {
			Engine.Raise(err)
			return
		}
		// Promote any fully-downloaded background refresh from a previous
		// session BEFORE the pck is mounted, so this run serves fresh bytes.
		crl.promotePendingRefresh(local)
		if _, err := local.Seek(0, io.SeekStart); err != nil {
			Engine.Raise(err)
			return
		}
		// Reserve slots only for files we cannot already serve (new/missing);
		// files we hold usable but stale bytes for are left in place so the
		// mount serves them without blocking — they refresh in the background.
		if _, err := pck.AppendMissing(local, crl.cloud); err != nil {
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
		// Files we hold usable (non-missing) bytes for but whose cloud hash has
		// changed: serve the stale bytes now, re-download them in the background.
		for p, cf := range crl.cloud {
			if lf, ok := crl.local[p]; ok && !lf.Missing() && lf.Hash != cf.Hash {
				stale = append(stale, refreshItem{path: p, cloud: cf})
			}
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
	// Kick off the background refresh. The goroutine uses its own file handles
	// and cloud connections, so it is independent of the handles closed when
	// this function returns, and computes its own append offset under refreshMu.
	if len(stale) > 0 {
		go crl.backgroundRefresh(stale)
	}
}

// refreshItem is one stale-but-present file to re-download in the background:
// its path plus the cloud slot (Seek/Size/Hash) to copy fresh bytes from.
type refreshItem struct {
	path  string
	cloud pck.File
}

// backgroundRefresh re-downloads the given stale files concurrently into freshly
// appended slots in the local library.pck, then — only if every file succeeded —
// records them in the refresh sidecar so the next launch can Promote them atomically.
//
// It never touches the directory Godot has already mounted nor the maps the
// resource thread reads, so it is safe to run alongside on-demand downloads:
// each worker writes a disjoint, newly-appended byte range through its own file
// handle (offsets allocated under mutex), and the commit happens single-threaded
// in load() on the next launch. An incomplete run (error or shutdown) simply
// writes no sidecar, so the stale files are retried next launch.
func (crl *CommunityResourceLoader) backgroundRefresh(items []refreshItem) {
	const workers = 6

	crl.refreshMu.Lock()
	defer crl.refreshMu.Unlock()

	// Append after the current end-of-file. Nothing else grows library.pck:
	// on-demand downloads only fill already-reserved slots, and refreshMu keeps
	// any other refresh from appending concurrently.
	fi, err := os.Stat(UserDataDir + "/library.pck")
	if err != nil {
		Engine.Raise(err)
		return
	}

	var mu sync.Mutex
	end := fi.Size()
	alloc := func(size int64) int64 {
		mu.Lock()
		defer mu.Unlock()
		off := end
		end += size
		return off
	}

	type result struct {
		path string
		slot pck.File
		ok   bool
	}
	jobs := make(chan refreshItem)
	out := make(chan result)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := httpseek.New(libraryURL)
			if err != nil {
				for it := range jobs {
					out <- result{path: it.path}
				}
				return
			}
			defer conn.Close()
			f, err := os.OpenFile(UserDataDir+"/library.pck", os.O_RDWR, 0644)
			if err != nil {
				for it := range jobs {
					out <- result{path: it.path}
				}
				return
			}
			defer f.Close()
			for it := range jobs {
				slot := pck.File{Seek: alloc(it.cloud.Size), Size: it.cloud.Size, Hash: it.cloud.Hash}
				if err := pck.Remap(f, conn, slot, it.cloud); err != nil {
					out <- result{path: it.path}
					continue
				}
				out <- result{path: it.path, slot: slot, ok: true}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, it := range items {
			select {
			case <-ShuttingDown:
				return
			case jobs <- it:
			}
		}
	}()
	go func() {
		wg.Wait()
		close(out)
	}()

	updates := make(map[string]pck.File, len(items))
	for r := range out {
		if r.ok {
			updates[r.path] = r.slot
		}
	}
	if len(updates) != len(items) {
		return // incomplete (error or shutdown): no promotion, retried next launch
	}
	if err := crl.writeRefreshSidecar(updates); err != nil {
		Engine.Raise(err)
	}
}

// writeRefreshSidecar records completed background-refresh slots atomically
// (temp file + rename), so the sidecar only ever exists in a complete state.
func (crl *CommunityResourceLoader) writeRefreshSidecar(updates map[string]pck.File) error {
	final := UserDataDir + "/" + refreshSidecarName
	tmp := final + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	if err := errors.Join(pck.WriteManifest(w, updates), w.Flush(), f.Close()); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, final)
}

// promotePendingRefresh applies a completed background refresh from a previous
// session into the (not-yet-mounted) local pck, then removes the sidecar. A
// recorded slot is only promoted if the cloud still serves that exact hash; if
// the cloud moved on again the file is left stale to be refreshed afresh.
// crl.cloud must already be indexed. Best-effort: any error leaves the local
// pck serving its current (stale) bytes.
func (crl *CommunityResourceLoader) promotePendingRefresh(local *os.File) {
	final := UserDataDir + "/" + refreshSidecarName
	f, err := os.Open(final)
	if err != nil {
		return // no pending refresh
	}
	recorded, err := pck.ReadManifest(bufio.NewReader(f))
	f.Close()
	if err != nil {
		os.Remove(final) // corrupt sidecar: drop it, files refresh again
		return
	}
	updates := make(map[string]pck.File, len(recorded))
	for name, slot := range recorded {
		if cf, ok := crl.cloud[name]; ok && cf.Hash == slot.Hash {
			updates[name] = slot
		}
	}
	if len(updates) > 0 {
		if _, err := local.Seek(0, io.SeekStart); err != nil {
			Engine.Raise(err)
		} else if err := pck.Promote(local, updates); err != nil {
			Engine.Raise(fmt.Errorf("failed to promote refreshed library.pck: %w", err))
		}
	}
	os.Remove(final)
}

// indexPCKFile opens a pck file read-only and returns its directory index.
func indexPCKFile(path string) (map[string]pck.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return pck.Index(f)
}

// fetchMissing concurrently downloads the listed entries from the cloud pck at
// url into their already-reserved (missing) slots in the local pck file at
// localPath, each via an HTTP range request over [httpseek]. progress, if
// non-nil, is called with the running byte total as each entry completes. It
// returns the first error encountered (the caller decides whether to fall back
// to a full copy). Each worker uses its own cloud connection and file handle,
// writing disjoint, already-reserved slots, so they never collide.
func fetchMissing(localPath, url string, cloud, local map[string]pck.File, missing []string, progress func(total int64)) error {
	const workers = 8
	jobs := make(chan string)
	var (
		mu       sync.Mutex
		total    int64
		firstErr error
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := httpseek.New(url)
			if err != nil {
				fail(err)
				for range jobs { // drain so producer is not blocked
				}
				return
			}
			defer conn.Close()
			f, err := os.OpenFile(localPath, os.O_RDWR, 0644)
			if err != nil {
				fail(err)
				for range jobs {
				}
				return
			}
			defer f.Close()
			for p := range jobs {
				if err := pck.Remap(f, conn, local[p], cloud[p]); err != nil {
					fail(err)
					continue
				}
				if progress != nil {
					mu.Lock()
					total += cloud[p].Size
					n := total
					mu.Unlock()
					progress(n)
				}
			}
		}()
	}
	for _, p := range missing {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	return firstErr
}
