package httpseek

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// URL is a struct that implements io.ReadSeekCloser.
// It uses HTTP range requests to fetch content from a URL on demand,
// without loading the entire content into memory.
type URL struct {
	url            string
	client         *http.Client
	contentLength  int64
	currentPos     int64
	reader         io.ReadCloser
	supportsRanges bool
	closed         bool
}

// New creates a new URLRangeReader by performing a HEAD request
// to verify range support and get the content length.
func New(url string) (*URL, time.Time, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, time.Time{}, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, time.Time{}, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, time.Time{}, fmt.Errorf("HEAD request failed: status %d", resp.StatusCode)
	}

	acceptRanges := resp.Header.Get("Accept-Ranges")
	if acceptRanges != "bytes" {
		return nil, time.Time{}, fmt.Errorf("server does not support byte-range requests")
	}

	contentLengthStr := resp.Header.Get("Content-Length")
	contentLength, err := strconv.ParseInt(contentLengthStr, 10, 64)
	if err != nil || contentLength <= 0 {
		return nil, time.Time{}, fmt.Errorf("invalid or missing Content-Length")
	}

	lastModStr := resp.Header.Get("Last-Modified")
	var lastMod time.Time
	if lastModStr != "" {
		lastMod, err = time.Parse(http.TimeFormat, lastModStr)
		if err != nil {
			// If parsing fails, log or handle appropriately; here we ignore and return zero time
			fmt.Printf("Warning: Failed to parse Last-Modified: %v\n", err)
			lastMod = time.Time{}
		}
	}

	return &URL{
		url:            url,
		client:         client,
		contentLength:  contentLength,
		reader:         resp.Body,
		currentPos:     0,
		supportsRanges: true,
	}, lastMod, nil
}

// Read reads up to len(p) bytes into p from the current body.
func (u *URL) Read(p []byte) (int, error) {
	if u.closed {
		return 0, fmt.Errorf("URLRangeReader is closed")
	}

	if u.currentPos >= u.contentLength {
		return 0, io.EOF
	}

	if u.reader == nil {
		req, err := http.NewRequest("GET", u.url, nil)
		if err != nil {
			return 0, err
		}

		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", u.currentPos))

		resp, err := u.client.Do(req)
		if err != nil {
			return 0, err
		}

		if resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			return 0, fmt.Errorf("expected status 206 Partial Content, got %d", resp.StatusCode)
		}
		expectedLen := u.contentLength - u.currentPos
		if resp.ContentLength > 0 && resp.ContentLength != expectedLen {
			resp.Body.Close()
			return 0, fmt.Errorf("range response Content-Length mismatch: got %d, expected %d", resp.ContentLength, expectedLen)
		}

		u.reader = resp.Body
	}

	n, err := u.reader.Read(p)
	u.currentPos += int64(n)

	if err != nil && err != io.EOF {
		u.reader.Close()
		u.reader = nil
		return n, err
	}

	if err == io.EOF && u.currentPos < u.contentLength {
		u.reader.Close()
		u.reader = nil
	}

	return n, err
}

// Seek creates a new HTTP range request to seek to the specified offset.
func (u *URL) Seek(offset int64, whence int) (int64, error) {
	if u.closed {
		return 0, fmt.Errorf("URLRangeReader is closed")
	}

	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = u.currentPos + offset
	case io.SeekEnd:
		newPos = u.contentLength + offset
	default:
		return 0, fmt.Errorf("invalid whence value")
	}

	if newPos < 0 {
		return 0, fmt.Errorf("seek position cannot be negative")
	}

	if newPos > u.contentLength {
		return 0, fmt.Errorf("seek position beyond content length")
	}

	if newPos != u.currentPos {
		if u.reader != nil {
			u.reader.Close()
			u.reader = nil
		}
		u.currentPos = newPos
	}

	return u.currentPos, nil
}

// Close closes the body.
func (u *URL) Close() error {
	if u.closed {
		return fmt.Errorf("URLRangeReader already closed")
	}
	u.closed = true
	if u.reader != nil {
		return u.reader.Close()
	}
	return nil
}
