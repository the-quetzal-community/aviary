package internal

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Label"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/ProgressBar"
	"graphics.gd/classdb/ProjectSettings"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/variant/Float"
	"the.quetzal.community/aviary/internal/datasize"
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
	req, err := http.NewRequest("HEAD", "https://vpk.quetzal.community/library.pck", nil)
	if err != nil {
		Engine.Raise(err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		Engine.Raise(err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		Engine.Raise(errors.New("failed to fetch library.pck: " + resp.Status))
		return
	}
	dl.setContentLength(resp)
	dl.DownloadButton.AsBaseButton().OnPressed(func() {
		if dl.downloading {
			return
		}
		dl.downloading = true
		dl.DownloadButton.Pointer.AsCanvasItem().SetVisible(false)
		dl.Progress.AsCanvasItem().SetVisible(true)
		resp, err := http.Get("https://vpk.quetzal.community/library.pck")
		if err != nil {
			Engine.Raise(err)
			return
		}
		dl.setContentLength(resp)
		go dl.download(resp.Body)
		dl.DownloadButton.Size.SetText("Downloading...")
	})
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
	library, err := os.Create(filepath.Join(OS.GetUserDataDir(), "library.pck"))
	if err != nil {
		Engine.Raise(err)
		return
	}
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
		ProjectSettings.LoadResourcePack("user://library.pck", 0)
		SceneTree.Add(NewClient())
		dl.AsNode().QueueFree()
		return
	default:
	}
}
