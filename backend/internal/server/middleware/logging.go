package middleware

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// ResponseCapture observes a response without inventing optional transport
// interfaces. Use WrapResponseWriter to obtain the writer passed to handlers.
type ResponseCapture struct {
	http.ResponseWriter
	statusCode int
	bytes      int64
	writeErr   error
}

func (rw *ResponseCapture) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

func (rw *ResponseCapture) Status() int {
	if rw.statusCode == 0 {
		return http.StatusOK
	}
	return rw.statusCode
}
func (rw *ResponseCapture) BytesWritten() int64 { return rw.bytes }
func (rw *ResponseCapture) WriteError() error   { return rw.writeErr }

func (rw *ResponseCapture) WriteHeader(code int) {
	// Informational responses do not consume the final response header. 101 is
	// final because the connection leaves the HTTP response protocol.
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		if rw.statusCode == 0 {
			rw.ResponseWriter.WriteHeader(code)
		}
		return
	}
	if rw.statusCode != 0 {
		return
	}
	rw.ResponseWriter.WriteHeader(code)
	rw.statusCode = code
}

func (rw *ResponseCapture) Write(p []byte) (int, error) {
	if rw.statusCode == 0 {
		rw.WriteHeader(http.StatusOK)
	}
	n, err := rw.ResponseWriter.Write(p)
	rw.bytes += int64(n)
	if err != nil && rw.writeErr == nil {
		rw.writeErr = err
	}
	return n, err
}

func (rw *ResponseCapture) reportHTTPStatus(code int) {
	if rw.statusCode == 0 {
		rw.statusCode = code
	}
}

// ReportHTTPStatus records a status written directly to a hijacked connection.
// The terminal handler calls this only after a successful WebSocket upgrade.
func ReportHTTPStatus(w http.ResponseWriter, code int) {
	for w != nil {
		if capture, ok := w.(interface{ reportHTTPStatus(int) }); ok {
			capture.reportHTTPStatus(code)
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = unwrapper.Unwrap()
	}
}

type captureFlusher struct {
	capture  *ResponseCapture
	original http.Flusher
}

func (f captureFlusher) Flush() {
	if f.capture.statusCode == 0 {
		f.capture.WriteHeader(http.StatusOK)
	}
	f.original.Flush()
}

type captureHijacker struct{ original http.Hijacker }

func (h captureHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) { return h.original.Hijack() }

type capturePusher struct{ original http.Pusher }

func (p capturePusher) Push(target string, opts *http.PushOptions) error {
	return p.original.Push(target, opts)
}

type captureReaderFrom struct {
	capture  *ResponseCapture
	original io.ReaderFrom
}

func (r captureReaderFrom) ReadFrom(source io.Reader) (int64, error) {
	if r.capture.statusCode == 0 {
		r.capture.WriteHeader(http.StatusOK)
	}
	n, err := r.original.ReadFrom(source)
	r.capture.bytes += n
	if err != nil && r.capture.writeErr == nil {
		r.capture.writeErr = err
	}
	return n, err
}

// WrapResponseWriter preserves precisely the interfaces implemented by w.
// Promoted interface adapters make each optional capability independent.
func WrapResponseWriter(w http.ResponseWriter) (http.ResponseWriter, *ResponseCapture) {
	capture := &ResponseCapture{ResponseWriter: w}
	mask := 0
	flusher, hasFlush := w.(http.Flusher)
	hijacker, hasHijack := w.(http.Hijacker)
	pusher, hasPush := w.(http.Pusher)
	readerFrom, hasReadFrom := w.(io.ReaderFrom)
	if hasFlush {
		mask |= 1
	}
	if hasHijack {
		mask |= 2
	}
	if hasPush {
		mask |= 4
	}
	if hasReadFrom {
		mask |= 8
	}
	f := captureFlusher{capture, flusher}
	h := captureHijacker{hijacker}
	p := capturePusher{pusher}
	r := captureReaderFrom{capture, readerFrom}
	switch mask {
	case 1:
		return &struct {
			*ResponseCapture
			http.Flusher
		}{capture, f}, capture
	case 2:
		return &struct {
			*ResponseCapture
			http.Hijacker
		}{capture, h}, capture
	case 3:
		return &struct {
			*ResponseCapture
			http.Flusher
			http.Hijacker
		}{capture, f, h}, capture
	case 4:
		return &struct {
			*ResponseCapture
			http.Pusher
		}{capture, p}, capture
	case 5:
		return &struct {
			*ResponseCapture
			http.Flusher
			http.Pusher
		}{capture, f, p}, capture
	case 6:
		return &struct {
			*ResponseCapture
			http.Hijacker
			http.Pusher
		}{capture, h, p}, capture
	case 7:
		return &struct {
			*ResponseCapture
			http.Flusher
			http.Hijacker
			http.Pusher
		}{capture, f, h, p}, capture
	case 8:
		return &struct {
			*ResponseCapture
			io.ReaderFrom
		}{capture, r}, capture
	case 9:
		return &struct {
			*ResponseCapture
			http.Flusher
			io.ReaderFrom
		}{capture, f, r}, capture
	case 10:
		return &struct {
			*ResponseCapture
			http.Hijacker
			io.ReaderFrom
		}{capture, h, r}, capture
	case 11:
		return &struct {
			*ResponseCapture
			http.Flusher
			http.Hijacker
			io.ReaderFrom
		}{capture, f, h, r}, capture
	case 12:
		return &struct {
			*ResponseCapture
			http.Pusher
			io.ReaderFrom
		}{capture, p, r}, capture
	case 13:
		return &struct {
			*ResponseCapture
			http.Flusher
			http.Pusher
			io.ReaderFrom
		}{capture, f, p, r}, capture
	case 14:
		return &struct {
			*ResponseCapture
			http.Hijacker
			http.Pusher
			io.ReaderFrom
		}{capture, h, p, r}, capture
	case 15:
		return &struct {
			*ResponseCapture
			http.Flusher
			http.Hijacker
			http.Pusher
			io.ReaderFrom
		}{capture, f, h, p, r}, capture
	default:
		return capture, capture
	}
}

// Logger logs bounded request facts and retains streaming writer capabilities.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped, capture := WrapResponseWriter(w)
		next.ServeHTTP(wrapped, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", capture.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", GetRequestID(r.Context()),
		)
	})
}
