package httpseek

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// URL to an internet-hosted io.ReadSeekCloser. It uses HTTP range requests to fetch content
// from the URL on demand and attempts to reuse existing connections where possible.
type URL struct {
	url           string
	modifiedAt    time.Time
	contentLength int64
	currentPos    int64
	reader        io.ReadCloser
	closed        bool

	on_modified func(*URL)
}

// seekDiscardThreshold is the largest forward gap that [URL.Seek] will skip by
// reading and discarding bytes over the already-open connection instead of
// issuing a fresh range request. Larger gaps are cheaper to reach with a new
// range request than to download and throw away.
const seekDiscardThreshold = 1 << 16 // 64 KiB

// client is shared by every [URL] so keep-alive connections are pooled and
// reused across range requests, and across separate URLs. The Transport sets
// connection-setup and header timeouts; there is deliberately no Client.Timeout,
// which would bound the whole body read and abort large, legitimate downloads.
var client = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   4,
	},
}

// parseLastModified returns the parsed Last-Modified header, or the zero time
// if the header is absent or cannot be parsed.
func parseLastModified(h http.Header) time.Time {
	v := h.Get("Last-Modified")
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(http.TimeFormat, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

// New creates a new URL reader by performing an initial GET request to verify
// range support and read the content length. The response body is retained so
// that a read starting from the beginning reuses the same connection.
func New(url string) (*URL, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("range probe failed: status %d", resp.StatusCode)
	}
	acceptRanges := resp.Header.Get("Accept-Ranges")
	if acceptRanges != "bytes" {
		resp.Body.Close()
		return nil, fmt.Errorf("server does not support byte-range requests")
	}
	contentLengthStr := resp.Header.Get("Content-Length")
	contentLength, err := strconv.ParseInt(contentLengthStr, 10, 64)
	if err != nil || contentLength <= 0 {
		resp.Body.Close()
		return nil, fmt.Errorf("invalid or missing Content-Length")
	}
	return &URL{
		url:           url,
		contentLength: contentLength,
		reader:        resp.Body,
		currentPos:    0,
		modifiedAt:    parseLastModified(resp.Header),
	}, nil
}

// LastModifiedAt returns the time the underlying resource was last modified.
func (u *URL) LastModifiedAt() time.Time {
	return u.modifiedAt
}

// OnResourceModified runs the specified function whenever the underlying resource
// is changed in response to an IO operation. This provides an opportunity to handle
// any indexing or other adjustments needed to continue seeking correctly (ie. the
// size or structure of the resource may have changed).
func (u *URL) OnResourceModified(f func(*URL)) {
	u.on_modified = f
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
		resp, err := client.Do(req)
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

		lastMod := parseLastModified(resp.Header)
		u.reader = resp.Body
		if !lastMod.IsZero() && !lastMod.Equal(u.modifiedAt) {
			u.modifiedAt = lastMod
			if u.on_modified != nil {
				u.on_modified(u)
			}
		}
	}
	n, err := u.reader.Read(p)
	u.currentPos += int64(n)
	if err != nil && err != io.EOF {
		u.reader.Close()
		u.reader = nil
		return n, err
	}
	if err == io.EOF && u.currentPos < u.contentLength {
		// The body ended before the logical end of the resource (e.g. a
		// chunking proxy split the open-ended range). Drop the spent body and
		// report a short read with no error: the next Read reopens a fresh
		// range request from the current position and transparently resumes.
		u.reader.Close()
		u.reader = nil
		return n, nil
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
	if newPos == u.currentPos {
		return u.currentPos, nil
	}
	// Fast path: for a small forward seek while a response body is already
	// open, discard the intervening bytes to reuse the current connection and
	// save the round trip of a fresh range request. Larger gaps fall through to
	// a new range request issued lazily on the next Read.
	if u.reader != nil && newPos > u.currentPos && newPos-u.currentPos <= seekDiscardThreshold {
		if _, err := io.CopyN(io.Discard, u, newPos-u.currentPos); err != nil {
			return 0, err
		}
		return u.currentPos, nil
	}
	if u.reader != nil {
		u.reader.Close()
		u.reader = nil
	}
	u.currentPos = newPos
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
