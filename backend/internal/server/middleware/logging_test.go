package middleware

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"testing"
)

type plainResponse struct {
	header       http.Header
	statuses     []int
	body         bytes.Buffer
	writeErr     error
	flushed      bool
	pushed       bool
	hijacked     bool
	usedReadFrom bool
}

func (w *plainResponse) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *plainResponse) WriteHeader(status int) { w.statuses = append(w.statuses, status) }
func (w *plainResponse) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.body.Write(p)
}

type testFlusher struct{ w *plainResponse }

func (f testFlusher) Flush() { f.w.flushed = true }

type testHijacker struct{ w *plainResponse }

func (h testHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.w.hijacked = true
	return nil, nil, nil
}

type testPusher struct{ w *plainResponse }

func (p testPusher) Push(string, *http.PushOptions) error { p.w.pushed = true; return nil }

type testReaderFrom struct{ w *plainResponse }

func (r testReaderFrom) ReadFrom(source io.Reader) (int64, error) {
	r.w.usedReadFrom = true
	return io.Copy(&r.w.body, source)
}

func testCapabilities(w *plainResponse, mask int) http.ResponseWriter {
	f, h, p, r := testFlusher{w}, testHijacker{w}, testPusher{w}, testReaderFrom{w}
	switch mask {
	case 1:
		return &struct {
			*plainResponse
			http.Flusher
		}{w, f}
	case 2:
		return &struct {
			*plainResponse
			http.Hijacker
		}{w, h}
	case 3:
		return &struct {
			*plainResponse
			http.Flusher
			http.Hijacker
		}{w, f, h}
	case 4:
		return &struct {
			*plainResponse
			http.Pusher
		}{w, p}
	case 5:
		return &struct {
			*plainResponse
			http.Flusher
			http.Pusher
		}{w, f, p}
	case 6:
		return &struct {
			*plainResponse
			http.Hijacker
			http.Pusher
		}{w, h, p}
	case 7:
		return &struct {
			*plainResponse
			http.Flusher
			http.Hijacker
			http.Pusher
		}{w, f, h, p}
	case 8:
		return &struct {
			*plainResponse
			io.ReaderFrom
		}{w, r}
	case 9:
		return &struct {
			*plainResponse
			http.Flusher
			io.ReaderFrom
		}{w, f, r}
	case 10:
		return &struct {
			*plainResponse
			http.Hijacker
			io.ReaderFrom
		}{w, h, r}
	case 11:
		return &struct {
			*plainResponse
			http.Flusher
			http.Hijacker
			io.ReaderFrom
		}{w, f, h, r}
	case 12:
		return &struct {
			*plainResponse
			http.Pusher
			io.ReaderFrom
		}{w, p, r}
	case 13:
		return &struct {
			*plainResponse
			http.Flusher
			http.Pusher
			io.ReaderFrom
		}{w, f, p, r}
	case 14:
		return &struct {
			*plainResponse
			http.Hijacker
			http.Pusher
			io.ReaderFrom
		}{w, h, p, r}
	case 15:
		return &struct {
			*plainResponse
			http.Flusher
			http.Hijacker
			http.Pusher
			io.ReaderFrom
		}{w, f, h, p, r}
	default:
		return w
	}
}

func TestWriterPreservesOptionalCapabilities(t *testing.T) {
	for mask := 0; mask < 16; mask++ {
		original := &plainResponse{}
		writer, capture := WrapResponseWriter(testCapabilities(original, mask))
		_, flush := writer.(http.Flusher)
		_, hijack := writer.(http.Hijacker)
		_, push := writer.(http.Pusher)
		_, readFrom := writer.(io.ReaderFrom)
		got := []bool{flush, hijack, push, readFrom}
		want := []bool{mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("mask %d capabilities = %v, want %v", mask, got, want)
		}
		if flush {
			writer.(http.Flusher).Flush()
			if !original.flushed {
				t.Fatal("Flush was not forwarded")
			}
		}
		if hijack {
			_, _, err := writer.(http.Hijacker).Hijack()
			if err != nil || !original.hijacked {
				t.Fatal("Hijack was not forwarded")
			}
		}
		if push {
			if err := writer.(http.Pusher).Push("/safe", nil); err != nil || !original.pushed {
				t.Fatal("Push was not forwarded")
			}
		}
		if readFrom {
			n, err := writer.(io.ReaderFrom).ReadFrom(bytes.NewBufferString("stream"))
			if err != nil || n != 6 || capture.BytesWritten() != 6 || !original.usedReadFrom {
				t.Fatalf("ReadFrom = %d,%v", n, err)
			}
		}
	}
}

func TestWriterFinalStatusAndImplicitWrites(t *testing.T) {
	original := &plainResponse{}
	writer, capture := WrapResponseWriter(original)
	writer.WriteHeader(103)
	writer.WriteHeader(102)
	writer.WriteHeader(http.StatusCreated)
	writer.WriteHeader(http.StatusInternalServerError)
	_, _ = writer.Write([]byte("ok"))
	if capture.Status() != 201 || !reflect.DeepEqual(original.statuses, []int{103, 102, 201}) {
		t.Fatalf("status=%d headers=%v", capture.Status(), original.statuses)
	}
	if capture.BytesWritten() != 2 {
		t.Fatal("missing byte count")
	}

	original = &plainResponse{}
	writer, capture = WrapResponseWriter(original)
	_, _ = writer.Write([]byte("implicit"))
	if capture.Status() != 200 || !reflect.DeepEqual(original.statuses, []int{200}) {
		t.Fatal("implicit status not observed")
	}
}

func TestWriterHijackedUpgradeStatusPropagates(t *testing.T) {
	base := &plainResponse{}
	inner, innerCapture := WrapResponseWriter(testCapabilities(base, 2))
	outer, outerCapture := WrapResponseWriter(inner)
	_, _, _ = outer.(http.Hijacker).Hijack()
	if outerCapture.Status() != 200 {
		t.Fatal("Hijack must not invent upgrade success")
	}
	ReportHTTPStatus(outer, 101)
	if innerCapture.Status() != 101 || outerCapture.Status() != 101 {
		t.Fatal("101 did not reach both capture layers")
	}
	if len(base.statuses) != 0 {
		t.Fatal("status report attempted HTTP write after hijack")
	}
}

func TestWriterKeepsFirstWriteError(t *testing.T) {
	failure := errors.New("transport failure")
	base := &plainResponse{writeErr: failure}
	writer, capture := WrapResponseWriter(base)
	_, err := writer.Write([]byte("body"))
	if !errors.Is(err, failure) || !errors.Is(capture.WriteError(), failure) {
		t.Fatal("write error lost")
	}
}
