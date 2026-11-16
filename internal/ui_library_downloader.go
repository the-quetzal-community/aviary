package internal

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

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
	"graphics.gd/classdb/Tween"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector2"
	"the.quetzal.community/aviary/internal/datasize"
	"the.quetzal.community/aviary/internal/httpseek"
	"the.quetzal.community/aviary/internal/netstate"
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
	checking         bool // computePlan is running in the background
	total            datasize.ByteSize
	bytes_downloaded chan datasize.ByteSize
	done             chan struct{}
	plan             *previewPlan

	// failed is set when a size check or download didn't complete; it is
	// retried automatically at retryAt (sooner if the internet comes back) or
	// on the next press. retryDelay doubles per consecutive failure and is
	// zero after a success.
	failed     bool
	retryAt    time.Time
	retryDelay time.Duration

	offlineDot   TextureRect.Instance // pulsing red dot while the internet is unreachable
	offlinePulse Tween.Instance
	netGen       uint64
}

const (
	minRetryDelay = 2 * time.Second
	maxRetryDelay = 30 * time.Second
)

// previewPlan is the result of checking the cloud preview.pck against any local
// backup: how many bytes actually need downloading and whether that can be done
// incrementally (reusing the backup) or needs a full fetch. Computed in the
// background when the page appears so the button shows the real size up front.
type previewPlan struct {
	cloudIdx      map[string]pck.File
	previewPath   string
	backupPath    string
	downloadBytes int64
	incremental   bool
}

func (dl *LibraryDownloader) Ready() {
	dl.bytes_downloaded = make(chan datasize.ByteSize, 1)
	dl.done = make(chan struct{}, 1)
	dl.offlineDot = newOfflineDot(36)
	dl.DownloadButton.AsNode().AddChild(dl.offlineDot.AsNode())
	dl.offlineDot.AsControl().SetPosition(Vector2.New(100, -8)) // top-right corner of the 128px button
	dl.netGen = netstate.Generation()
	dl.applyNetState()
	dl.check()
	dl.DownloadButton.AsBaseButton().OnPressed(func() {
		if dl.downloading {
			return
		}
		if dl.plan == nil {
			// Still sizing: ignore until the size is known. After a failure the
			// button shakes (nothing started) and the check is retried now.
			if dl.failed {
				shakeRefused(dl.DownloadButton.AsControl())
				dl.check()
			}
			return
		}
		dl.downloading = true
		dl.DownloadButton.Pointer.AsCanvasItem().SetVisible(false)
		go dl.execute(*dl.plan)
	})
}

// check reads the cloud directory in the background and diffs it against any
// local backup, so the button shows the REAL download size (just the delta for
// an update) before it is pressed. The bar is indeterminate until then.
func (dl *LibraryDownloader) check() {
	dl.checking = true
	dl.failed = false
	dl.Progress.AsCanvasItem().SetVisible(true)
	dl.Progress.SetIndeterminate(true)
	dl.DownloadButton.Size.SetText("")
	go func() {
		plan, err := dl.computePlan()
		Callable.Defer(Callable.New(func() {
			dl.checking = false
			if err != nil {
				dl.fail(err)
				return
			}
			netstate.MarkOnline()
			dl.retryDelay = 0
			dl.plan = &plan
			if plan.downloadBytes == 0 {
				// Already up to date — nothing to fetch. Restore the backup and
				// continue to the client without a click.
				dl.downloading = true
				dl.DownloadButton.Pointer.AsCanvasItem().SetVisible(false)
				go dl.execute(plan)
				return
			}
			dl.total = datasize.ByteSize(plan.downloadBytes)
			dl.Progress.SetIndeterminate(false)
			dl.Progress.AsRange().SetMaxValue(Float.X(plan.downloadBytes))
			dl.Progress.AsRange().SetValue(0)
			dl.DownloadButton.Size.SetText(datasize.ByteSize(plan.downloadBytes).HumanReadable())
		}))
	}()
}

// fail handles a size check or download that didn't complete (err may be nil
// when the cause was already recorded). Being offline is an expected state the
// red dot shows; anything else is reported, once per run of failures so the
// automatic retries don't flood Sentry. The bar stops spinning and the
// attempt is scheduled again with backoff. Main thread only.
func (dl *LibraryDownloader) fail(err error) {
	if err != nil {
		if dl.retryDelay == 0 {
			raiseUnlessOffline(err)
		} else {
			netstate.Check(err)
		}
	}
	dl.failed = true
	dl.downloading = false
	dl.plan = nil
	dl.total = 0
	dl.Progress.SetIndeterminate(false)
	dl.Progress.AsRange().SetValue(0)
	dl.DownloadButton.Size.SetText("")
	dl.DownloadButton.Pointer.AsCanvasItem().SetVisible(true)
	dl.retryDelay = min(max(dl.retryDelay*2, minRetryDelay), maxRetryDelay)
	dl.retryAt = time.Now().Add(dl.retryDelay)
}

// failLater marshals [LibraryDownloader.fail] onto the main thread.
func (dl *LibraryDownloader) failLater(err error) {
	Callable.Defer(Callable.New(func() { dl.fail(err) }))
}

// applyNetState shows or hides the pulsing red "no internet" dot.
func (dl *LibraryDownloader) applyNetState() {
	offline := !netstate.Online()
	dl.offlineDot.AsCanvasItem().SetVisible(offline)
	switch {
	case offline && dl.offlinePulse == Tween.Nil:
		dl.offlinePulse = pulseOffline(dl.offlineDot.AsCanvasItem())
	case !offline && dl.offlinePulse != Tween.Nil:
		dl.offlinePulse.Kill()
		dl.offlinePulse = Tween.Nil
	}
}

// computePlan reads the cloud preview directory and, if a local backup exists,
// diffs it (by content hash) to size an incremental update. It does NOT modify
// any files — just decides how much would download and whether it can reuse the
// backup. Runs on a goroutine; the caller marshals UI updates to the main thread.
func (dl *LibraryDownloader) computePlan() (previewPlan, error) {
	previewPath := filepath.Join(UserDataDir, "preview.pck")
	plan := previewPlan{previewPath: previewPath, backupPath: previewPath + ".backup"}
	cloud, err := httpseek.New(previewURL)
	if err != nil {
		return plan, err
	}
	plan.cloudIdx, err = pck.Index(cloud)
	cloud.Close()
	if err != nil {
		return plan, err
	}
	var totalBytes int64
	for _, e := range plan.cloudIdx {
		totalBytes += e.Size
	}
	plan.downloadBytes = totalBytes // default: full download
	if fi, err := os.Stat(plan.backupPath); err == nil && fi.Size() > 0 {
		if baseIdx, err := indexPCKFile(plan.backupPath); err == nil {
			var missingBytes int64
			for p, cf := range plan.cloudIdx {
				if bf, ok := baseIdx[p]; !ok || bf.Hash != cf.Hash {
					missingBytes += cf.Size
				}
			}
			// Worth ranging only if it saves a meaningful fraction; otherwise a
			// single full stream beats thousands of range requests.
			if missingBytes*2 <= totalBytes {
				plan.incremental = true
				plan.downloadBytes = missingBytes
			}
		}
	}
	return plan, nil
}

// execute performs the download decided by the plan, closing dl.done when done.
func (dl *LibraryDownloader) execute(plan previewPlan) {
	if plan.incremental {
		// Zero delta: nothing to fetch — just restore the backup as the current
		// preview and bump its modtime so the freshness check (velopack.go,
		// which compares the cloud Last-Modified against the local file's
		// modtime) does not immediately re-trigger this download next launch.
		if plan.downloadBytes == 0 {
			if dl.restoreBackup(plan) {
				close(dl.done)
				return
			}
		} else if dl.doIncremental(plan) {
			close(dl.done)
			return
		}
		// An incremental fetch that failed for lack of internet: a full
		// download would only fail the same way.
		if !netstate.Online() {
			dl.failLater(nil)
			return
		}
	}
	dl.fullDownload()
}

// restoreBackup renames the backup back into place and freshens its modtime.
func (dl *LibraryDownloader) restoreBackup(plan previewPlan) bool {
	if err := os.Rename(plan.backupPath, plan.previewPath); err != nil {
		return false
	}
	now := time.Now()
	if err := os.Chtimes(plan.previewPath, now, now); err != nil {
		Callable.Defer(Callable.New(func() { Engine.Raise(err) }))
	}
	return true
}

// doIncremental rebuilds preview.pck from the backup, fetching only the changed
// or new entries over [httpseek] range requests. Returns false on any failure
// so execute can fall back to a clean full download.
func (dl *LibraryDownloader) doIncremental(plan previewPlan) bool {
	if err := os.Rename(plan.backupPath, plan.previewPath); err != nil {
		return false
	}
	local, err := os.OpenFile(plan.previewPath, os.O_RDWR, 0644)
	if err != nil {
		return false
	}
	if _, err := local.Seek(0, io.SeekStart); err != nil {
		local.Close()
		return false
	}
	if err := pck.Append(local, plan.cloudIdx); err != nil {
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
	for p := range plan.cloudIdx {
		if lf, ok := localIdx[p]; ok && lf.Missing() {
			missing = append(missing, p)
		}
	}
	if err := fetchMissing(plan.previewPath, previewURL, plan.cloudIdx, localIdx, missing, func(n int64) {
		select {
		case dl.bytes_downloaded <- datasize.ByteSize(n):
		default:
		}
	}); err != nil {
		// Partial update: fall back to a clean full download (overwrites it).
		// Record offline synchronously, so execute sees it before deciding.
		if !netstate.Check(err) {
			Callable.Defer(Callable.New(func() { Engine.Raise(err) }))
		}
		return false
	}
	return true
}

// fullDownload streams the entire preview.pck into place (first run, or when
// most of it changed), closing dl.done when complete.
func (dl *LibraryDownloader) fullDownload() {
	resp, err := http.Get(previewURL)
	if err != nil {
		dl.failLater(err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		dl.failLater(fmt.Errorf("failed to download preview.pck: %s", resp.Status))
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
	dl.Progress.SetIndeterminate(false)
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
	previewPath := filepath.Join(UserDataDir, "preview.pck")
	library, err := os.Create(previewPath)
	if err != nil {
		dl.failLater(err)
		return
	}
	fmt.Println("Downloading preview.pck to", library.Name())
	_, err = io.Copy(library, reader)
	if closeErr := library.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		// Don't leave a truncated pck behind: the next launch would mount it.
		os.Remove(previewPath)
		dl.failLater(err)
		return
	}
	netstate.MarkOnline()
	close(dl.done)
}

func (dl *LibraryDownloader) Process(delta Float.X) {
	if gen := netstate.Generation(); gen != dl.netGen {
		dl.netGen = gen
		dl.applyNetState()
		if netstate.Online() && dl.failed {
			dl.retryAt = time.Time{} // back online: retry now, not after the backoff
		}
	}
	if dl.failed && !dl.checking && !dl.downloading && time.Now().After(dl.retryAt) {
		dl.check()
	}
	select {
	case bytes := <-dl.bytes_downloaded:
		// dl.total is 0 while check() is still sizing the update (bar is
		// indeterminate); don't render a bogus "remaining" until it's known.
		if dl.total == 0 {
			break
		}
		dl.Progress.AsRange().SetValue(Float.X(bytes))
		remaining := datasize.ByteSize(0)
		if bytes < dl.total {
			remaining = dl.total - bytes
		}
		dl.DownloadButton.Size.SetText(remaining.HumanReadable())
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
