package internal

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/Label"
	"graphics.gd/classdb/ProgressBar"
	"graphics.gd/classdb/ProjectSettings"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Float"
	"the.quetzal.community/aviary/internal/datasize"
	"the.quetzal.community/aviary/internal/httpseek"
	"the.quetzal.community/aviary/internal/pck"
)

type LibraryDownloader struct {
	Control.Extension[LibraryDownloader] `gd:"AviaryLibraryDownloader"`

	DownloadButton struct {
		TextureButton.Instance

		Pointer TextureRect.Instance
		Size    Label.Instance
	}

	Progress ProgressBar.Instance

	downloading      bool
	total            datasize.ByteSize
	bytes_downloaded chan datasize.ByteSize
	done             chan struct{}
}

func (dl *LibraryDownloader) Ready() {
	dl.bytes_downloaded = make(chan datasize.ByteSize, 1)
	dl.done = make(chan struct{}, 1)
	dl.Progress.AsCanvasItem().SetVisible(false)
	// HEAD the .pck off-thread so a slow / unreachable host can't
	// block the splash-screen Ready. setContentLength touches UI
	// state so we marshal back to the main thread via Callable.Defer.
	go func() {
		req, err := http.NewRequest("HEAD", previewURL, nil)
		if err != nil {
			Callable.Defer(Callable.New(func() { Engine.Raise(err) }))
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			Callable.Defer(Callable.New(func() { Engine.Raise(err) }))
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			Callable.Defer(Callable.New(func() {
				Engine.Raise(errors.New("failed to fetch preview.pck: " + resp.Status))
			}))
			return
		}
		Callable.Defer(Callable.New(func() { dl.setContentLength(resp) }))
	}()
	dl.DownloadButton.AsBaseButton().OnPressed(func() {
		if dl.downloading {
			return
		}
		dl.downloading = true
		dl.DownloadButton.Pointer.AsCanvasItem().SetVisible(false)
		dl.Progress.AsCanvasItem().SetVisible(true)
		dl.DownloadButton.Size.SetText("Downloading...")
		go dl.run()
	})
}

// run downloads preview.pck. When a previous version was renamed aside to
// preview.pck.backup (by the freshness check in velopack.go), it reuses that
// version's unchanged entries and fetches only the changed/new ones over
// [httpseek] range requests — so the user sees the same loading bar with far
// fewer bytes to download. Otherwise (first run, or most of the pck changed)
// it falls back to a full download. Either way it closes dl.done when complete.
func (dl *LibraryDownloader) run() {
	previewPath := filepath.Join(UserDataDir, "preview.pck")
	backupPath := previewPath + ".backup"

	// Read the cloud directory once to learn what changed.
	cloud, err := httpseek.New(previewURL)
	if err != nil {
		Callable.Defer(Callable.New(func() { Engine.Raise(err) }))
		return
	}
	cloudIdx, err := pck.Index(cloud)
	cloud.Close()
	if err != nil {
		Callable.Defer(Callable.New(func() { Engine.Raise(err) }))
		return
	}
	var totalBytes int64
	for _, e := range cloudIdx {
		totalBytes += e.Size
	}

	if dl.tryIncremental(previewPath, backupPath, cloudIdx, totalBytes) {
		close(dl.done)
		return
	}
	dl.fullDownload()
}

// tryIncremental updates preview.pck in place from the renamed-aside backup,
// fetching only the entries whose content hash changed. It returns false (and
// leaves a clean fall-back to a full download) when there is no backup, the
// backup is unreadable, or so much changed that one full stream is cheaper.
func (dl *LibraryDownloader) tryIncremental(previewPath, backupPath string, cloudIdx map[string]pck.File, totalBytes int64) bool {
	fi, err := os.Stat(backupPath)
	if err != nil || fi.Size() == 0 {
		return false // no previous version to diff against
	}
	// Decide before touching anything: how much actually changed?
	baseIdx, err := indexPCKFile(backupPath)
	if err != nil {
		return false
	}
	var missingBytes int64
	for p, cf := range cloudIdx {
		if bf, ok := baseIdx[p]; !ok || bf.Hash != cf.Hash {
			missingBytes += cf.Size
		}
	}
	if missingBytes*2 > totalBytes {
		return false // most of the file changed; a single full stream is cheaper
	}

	// Commit: reuse the backup in place, reserve the changed/new slots, then
	// fetch just those into them.
	if err := os.Rename(backupPath, previewPath); err != nil {
		return false
	}
	local, err := os.OpenFile(previewPath, os.O_RDWR, 0644)
	if err != nil {
		return false
	}
	if _, err := local.Seek(0, io.SeekStart); err != nil {
		local.Close()
		return false
	}
	if err := pck.Append(local, cloudIdx); err != nil {
		local.Close()
		return false
	}
	if _, err := local.Seek(0, io.SeekStart); err != nil {
		local.Close()
		return false
	}
	localIdx, err := pck.Index(local)
	local.Close()
	if err != nil {
		return false
	}
	var missing []string
	for p := range cloudIdx {
		if lf, ok := localIdx[p]; ok && lf.Missing() {
			missing = append(missing, p)
		}
	}

	Callable.Defer(Callable.New(func() {
		dl.total = datasize.ByteSize(missingBytes)
		dl.Progress.AsRange().SetMaxValue(Float.X(missingBytes))
		dl.DownloadButton.Size.SetText(datasize.ByteSize(missingBytes).HumanReadable())
	}))
	if err := fetchMissing(previewPath, previewURL, cloudIdx, localIdx, missing, func(n int64) {
		select {
		case dl.bytes_downloaded <- datasize.ByteSize(n):
		default:
		}
	}); err != nil {
		// Partial update: fall back to a clean full download (overwrites it).
		Callable.Defer(Callable.New(func() { Engine.Raise(err) }))
		return false
	}
	return true
}

// fullDownload streams the entire preview.pck into place (first run, or when
// most of it changed), closing dl.done when complete.
func (dl *LibraryDownloader) fullDownload() {
	resp, err := http.Get(previewURL)
	if err != nil {
		Callable.Defer(Callable.New(func() { Engine.Raise(err) }))
		return
	}
	Callable.Defer(Callable.New(func() { dl.setContentLength(resp) }))
	dl.download(resp.Body)
}

func (dl *LibraryDownloader) setContentLength(resp *http.Response) {
	contentLength, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	if err != nil {
		Engine.Raise(err)
		return
	}
	dl.total = datasize.ByteSize(contentLength)
	dl.DownloadButton.Size.SetText(datasize.ByteSize(contentLength).HumanReadable())
	dl.Progress.AsRange().SetMaxValue(Float.X(contentLength))
}

type bytesCounter struct {
	count  datasize.ByteSize
	Notify chan datasize.ByteSize
}

func (wc *bytesCounter) Write(p []byte) (int, error) {
	n := len(p)
	wc.count += datasize.ByteSize(n)
	select {
	case wc.Notify <- wc.count:
	default:
	}
	return n, nil
}

func (dl *LibraryDownloader) download(body io.ReadCloser) {
	defer body.Close()
	counter := &bytesCounter{
		Notify: dl.bytes_downloaded,
	}
	reader := io.TeeReader(body, counter)
	fmt.Println("Starting download of preview.pck...")
	library, err := os.Create(filepath.Join(UserDataDir, "preview.pck"))
	if err != nil {
		Engine.Raise(err)
		return
	}
	fmt.Println("Downloading preview.pck to", library.Name())
	defer library.Close()
	if _, err := io.Copy(library, reader); err != nil {
		Engine.Raise(err)
		return
	}
	close(dl.done)
}

func (dl *LibraryDownloader) Process(delta Float.X) {
	select {
	case bytes := <-dl.bytes_downloaded:
		dl.Progress.AsRange().SetValue(Float.X(bytes))
		dl.DownloadButton.Size.SetText((dl.total - bytes).HumanReadable())
	case <-dl.done:
		if FileAccess.FileExists("res://preview.pck.backup") {
			DirAccess.RemoveAbsolute("res://preview.pck.backup")
		}
		if FileAccess.FileExists("user://preview.pck.backup") {
			DirAccess.RemoveAbsolute("user://preview.pck.backup")
		}
		ProjectSettings.LoadResourcePack("user://preview.pck", 0)
		SceneTree.Add(NewClient())
		dl.AsNode().QueueFree()
		return
	default:
	}
}
