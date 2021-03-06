package htpack

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lwithers/htpack/packed"
	"golang.org/x/sys/unix"
)

const (
	encodingGzip   = "gzip"
	encodingBrotli = "br"
)

// TODO: logging

// New returns a new handler. Standard security headers are set.
func New(packfile string) (*Handler, error) {
	f, err := os.Open(packfile)
	if err != nil {
		return nil, err
	}

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	mapped, err := unix.Mmap(int(f.Fd()), 0, int(fi.Size()),
		unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		f.Close()
		return nil, err
	}

	_, dir, err := packed.Load(f)
	if err != nil {
		unix.Munmap(mapped)
		f.Close()
		return nil, err
	}

	h := &Handler{
		f:         f,
		mapped:    mapped,
		dir:       dir.Files,
		headers:   make(map[string]string),
		startTime: time.Now(),
	}

	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/X-Frame-Options
	h.SetHeader("X-Frame-Options", "sameorigin")

	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/X-Content-Type-Options
	h.SetHeader("X-Content-Type-Options", "nosniff")

	return h, nil
}

// Handler implements http.Handler and allows options to be set.
type Handler struct {
	f         *os.File
	mapped    []byte
	dir       map[string]*packed.File
	headers   map[string]string
	startTime time.Time
}

// SetHeader allows a custom header to be set on HTTP responses. These are
// always emitted by ServeHTTP, whether the response status is success or
// otherwise. Note that you can override the standard security headers
// (X-Frame-Options and X-Content-Type-Options) using this function. You can
// remove previously-set headers altogether by passing an empty string for
// value.
func (h *Handler) SetHeader(key, value string) {
	if value == "" {
		delete(h.headers, key)
	} else {
		h.headers[key] = value
	}
}

// SetIndex allows setting an index.html (or equivalent) that can be used to
// serve requests landing at a directory. For instance, if a file named
// "/foo/index.html" exists, and this function is called with "index.html",
// then a route will be registered to serve the contents of this file at
// "/foo". Noting that the ServeHTTP handler discards a trailing "/" on non
// root URLs, this means that it will serve equivalent content for requests
// to "/foo/index.html", "/foo/" and "/foo".
//
// Existing routes are not overwritten, and this function could be called
// multiple times with different filenames (noting later calls would not
// overwrite files matching earlier calls).
func (h *Handler) SetIndex(filename string) {
	for k, v := range h.dir {
		if filepath.Base(k) == filename {
			routeToAdd := filepath.Dir(k)
			if _, exists := h.dir[routeToAdd]; !exists {
				h.dir[routeToAdd] = v
			}
		}
	}
}

// ServeHTTP handles requests for files. It supports GET and HEAD methods, with
// anything else returning a 405. Exact path matches are required, else a 404 is
// returned.
func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// set custom headers before any processing; ensures these are set even
	// on error responses
	for hkey, hval := range h.headers {
		w.Header().Set(hkey, hval)
	}

	switch req.Method {
	case "HEAD", "GET":
		// OK
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	info := h.dir[path.Clean(req.URL.Path)]
	if info == nil {
		http.NotFound(w, req)
		return
	}

	// set standard headers
	w.Header().Set("Vary", "Accept-Encoding")
	w.Header().Set("Etag", info.Etag)
	w.Header().Set("Content-Type", info.ContentType)
	w.Header().Set("Accept-Ranges", "bytes")

	// process etag / modtime
	if clientHasCachedVersion(info.Etag, h.startTime, req) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// select compression
	data := info.Uncompressed
	gzip, brotli := acceptedEncodings(req)
	if brotli && info.Brotli != nil {
		data = info.Brotli
		w.Header().Set("Content-Encoding", encodingBrotli)
	} else if gzip && info.Gzip != nil {
		data = info.Gzip
		w.Header().Set("Content-Encoding", encodingGzip)
	}

	// range support (single-part ranges only)
	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Range_requests#Single_part_ranges
	offset, length, isPartial := getFileRange(data, req)
	if isPartial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d",
			offset, offset+length-1, data.Length))
	}

	// now we know exactly what we're writing, finalise HTTP header
	w.Header().Set("Content-Length", strconv.FormatUint(length, 10))
	if isPartial {
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	// send body (though not for HEAD)
	if req.Method == "HEAD" {
		return
	}
	h.sendfile(w, data, offset, length)
}

func (h *Handler) sendfile(w http.ResponseWriter, data *packed.FileData,
	offset, length uint64,
) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		// fallback
		h.copyfile(w, data, offset, length)
		return
	}

	conn, buf, err := hj.Hijack()
	if err != nil {
		// fallback
		h.copyfile(w, data, offset, length)
		return
	}

	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		// fallback
		h.copyfile(w, data, offset, length)
		return
	}
	defer tcp.Close()

	rawsock, err := tcp.SyscallConn()
	if err == nil {
		err = buf.Flush()
	}
	if err != nil {
		// error only returned if the underlying connection is broken,
		// so there's no point calling sendfile
		return
	}

	var breakErr error
	off := int64(data.Offset + offset)
	remain := length

	for breakErr == nil && remain > 0 {
		// sendfile(2) can send a maximum of 1GiB
		var amt int
		if remain > (1 << 30) {
			amt = (1 << 30)
		} else {
			amt = int(remain)
		}

		// behaviour of control function:
		//  · some bytes written: sets written > 0, returns true (breaks
		//                        out of loop on first write)
		//  · EAGAIN: returns false (causes Write() to loop until
		//            success or permanent failure)
		//  · other error: sets breakErr
		var written int
		rawsock.Write(func(outfd uintptr) bool {
			written, err = unix.Sendfile(int(outfd), int(h.f.Fd()), &off, amt)
			switch err {
			case nil:
				return true
			case syscall.EAGAIN:
				return false
			default:
				breakErr = err
				return true
			}
		})

		// we may have had a partial write, or file may have been > 1GiB
		remain -= uint64(written)
	}
}

// copyfile is a fallback handler that uses write(2) on our memory-mapped data
// to push out the response.
func (h *Handler) copyfile(w http.ResponseWriter, data *packed.FileData,
	offset, length uint64,
) {
	offset += data.Offset
	w.Write(h.mapped[offset : offset+length])
}

func acceptedEncodings(req *http.Request) (gzip, brotli bool) {
	encodings := req.Header.Get("Accept-Encoding")
	for _, enc := range strings.Split(encodings, ",") {
		switch strings.TrimSpace(enc) {
		case encodingGzip:
			gzip = true
		case encodingBrotli:
			brotli = true
		}
	}
	return
}

// clientHasCachedVersion returns true if the client has a cached version of
// the resource. We'll check the etags presented by the client, but if etags
// are not present then we'll check the if-modified-since date.
func clientHasCachedVersion(etag string, startTime time.Time, req *http.Request,
) bool {
	checkEtags := req.Header.Get("If-None-Match")
	for _, check := range strings.Split(checkEtags, ",") {
		if etag == strings.TrimSpace(check) {
			// client knows the etag, so it has this version of the
			// resource cached already
			return true
		}
	}

	// if the client presented etags at all, we use that as our definitive
	// answer
	if _, sawEtags := req.Header["If-None-Match"]; sawEtags {
		return false
	}

	// check the timestamp the client last grabbed the resource
	cachedTime, err := http.ParseTime(req.Header.Get("If-Modified-Since"))
	if err != nil {
		return false
	}
	return cachedTime.After(startTime)
}

// getFileRange returns the byte offset and length of the file to serve, along
// with whether or not it's partial content.
func getFileRange(data *packed.FileData, req *http.Request) (offset, length uint64, isPartial bool) {
	length = data.Length

	// only accept "Range: bytes=…"
	r := req.Header.Get("Range")
	if !strings.HasPrefix(r, "bytes=") {
		return
	}
	r = strings.TrimPrefix(r, "bytes=")

	// only accept a single range, "from-to", mapping to interval [from,to]
	pos := strings.IndexByte(r, '-')
	if pos == -1 {
		return
	}
	sfrom, sto := r[:pos], r[pos+1:]
	from, err := strconv.ParseUint(sfrom, 10, 64)
	if err != nil {
		return
	}
	to, err := strconv.ParseUint(sto, 10, 64)
	if err != nil {
		return
	}

	// validate the interval lies within the file
	switch {
	case from > to,
		from >= data.Length,
		to >= data.Length:
		return
	}

	// all good
	offset = from
	length = to - from + 1
	isPartial = true
	return
}
