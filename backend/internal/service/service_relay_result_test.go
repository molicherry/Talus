package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
)

func TestRelayUpstreamErrorKeepsPassthroughAndRecordsFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "upstream body")
	}))
	defer upstream.Close()
	svc := NewServiceRelayService(nil, nil)
	writer := httptest.NewRecorder()
	result, err := svc.relayFromService(context.Background(), &model.Service{BaseURL: upstream.URL}, RelayInput{Method: "GET", Path: "/"}, writer)
	if err != nil {
		t.Fatal(err)
	}
	if writer.Code != http.StatusNotFound || writer.Body.String() != "upstream body" {
		t.Fatalf("passthrough status/body = %d %q", writer.Code, writer.Body.String())
	}
	if result.Outcome != "failed" || result.Reason != "relay_upstream_failed" || result.UpstreamStatus != 404 || result.BytesCopied != 13 || !result.HeadersWritten {
		t.Fatalf("relay result = %#v", result)
	}
}

type relayRoundTripFunc func(*http.Request) (*http.Response, error)

func (f relayRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type relayBrokenReader struct{}

func (relayBrokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestRelayCopyFailurePreservesCommittedHeadersAndPartialBytes(t *testing.T) {
	svc := NewServiceRelayService(nil, nil)
	svc.httpClient = &http.Client{Transport: relayRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(io.MultiReader(strings.NewReader("partial"), relayBrokenReader{}))}, nil
	})}
	writer := httptest.NewRecorder()
	result, err := svc.relayFromService(context.Background(), &model.Service{BaseURL: "http://upstream.invalid"}, RelayInput{Method: "GET"}, writer)
	if !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(result.CopyError, io.ErrUnexpectedEOF) {
		t.Fatalf("copy errors = %v, %v", err, result.CopyError)
	}
	if !result.HeadersWritten || result.BytesCopied != 7 || result.Outcome != "failed" || result.Reason != "relay_copy_failed" {
		t.Fatalf("relay result = %#v", result)
	}
	if writer.Code != 200 || writer.Body.String() != "partial" {
		t.Fatalf("committed response = %d %q", writer.Code, writer.Body.String())
	}
}

func TestRelayStreamsBeforeUpstreamCompletes(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "one")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "two")
	}))
	t.Cleanup(upstream.Close)
	svc := NewServiceRelayService(nil, nil)
	results := make(chan RelayResult, 1)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, _ := svc.relayFromService(r.Context(), &model.Service{BaseURL: upstream.URL}, RelayInput{Method: "GET"}, w)
		results <- result
	}))
	t.Cleanup(relay.Close)
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(relay.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := make([]byte, 3)
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatal(err)
	}
	if string(first) != "one" {
		t.Fatalf("first chunk = %q", first)
	}
	// Upstream is still parked, proving headers and the first chunk arrived
	// without waiting for the complete response.
	select {
	case <-results:
		t.Fatal("relay finished before upstream released")
	default:
	}
}

type relayHeaderNotifyWriter struct {
	*httptest.ResponseRecorder
	headers chan struct{}
}

func (w relayHeaderNotifyWriter) WriteHeader(status int) {
	w.ResponseRecorder.WriteHeader(status)
	close(w.headers)
}

func TestRelayCancellationAfterHeadersIsDistinctFromCopyFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "one")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	svc := NewServiceRelayService(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type completed struct {
		result RelayResult
		err    error
	}
	done := make(chan completed, 1)
	headers := make(chan struct{})
	writer := relayHeaderNotifyWriter{httptest.NewRecorder(), headers}
	go func() {
		result, err := svc.relayFromService(ctx, &model.Service{BaseURL: upstream.URL}, RelayInput{Method: "GET"}, writer)
		done <- completed{result, err}
	}()
	select {
	case <-headers:
	case <-time.After(2 * time.Second):
		t.Fatal("downstream headers did not arrive")
	}
	cancel()
	select {
	case completion := <-done:
		if completion.result.Outcome != "cancelled" || completion.result.Reason != "client_cancelled" || !completion.result.HeadersWritten || completion.err == nil {
			t.Fatalf("relay result = %#v, %v", completion.result, completion.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled relay did not finish")
	}
}
