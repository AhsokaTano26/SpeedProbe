package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T, mutate func(*Config)) *Server {
	t.Helper()
	c := DefaultConfig()
	c.Tokens = []string{"alpha", "beta"}
	c.MaxBytes = 128 << 10
	if mutate != nil {
		mutate(&c)
	}
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func request(t *testing.T, h http.Handler, method, path, token string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, body)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAuthentication(t *testing.T) {
	h := testServer(t, nil).Handler()
	for _, token := range []string{"", "wrong", "alpha", "beta"} {
		w := request(t, h, "GET", "/api/v1/ping", token, nil)
		want := http.StatusUnauthorized
		if token == "alpha" || token == "beta" {
			want = http.StatusNoContent
		}
		if w.Code != want {
			t.Errorf("token %q: got %d, want %d", token, w.Code, want)
		}
		if w.Code == 401 && w.Header().Get("WWW-Authenticate") == "" {
			t.Error("missing challenge")
		}
	}
	if w := request(t, h, "GET", "/healthz", "", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("GET", "/api/v1/ping", nil)
	r.Header.Add("Authorization", "Bearer alpha")
	r.Header.Add("Authorization", "Bearer beta")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("ambiguous auth accepted")
	}
}

func TestConfigurationFailsClosed(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Tokens = nil },
		func(c *Config) { c.Tokens = []string{""} },
		func(c *Config) { c.Tokens = []string{"a b"} },
		func(c *Config) { c.Tokens = []string{"alpha", "alpha"} },
		func(c *Config) { c.MaxConcurrent = 0 },
		func(c *Config) { c.MaxPerToken = -1 },
		func(c *Config) { c.MaxBytes = 0 },
		func(c *Config) { c.MaxDuration = 0 },
		func(c *Config) { c.BytesPerSecond = -1 },
	} {
		c := DefaultConfig()
		c.Tokens = []string{"alpha"}
		mutate(&c)
		if _, err := New(c); err == nil {
			t.Error("invalid config accepted")
		}
	}
}

func TestDownloadCapAndHeaders(t *testing.T) {
	w := request(t, testServer(t, nil).Handler(), "GET", "/api/v1/download?bytes=12345&duration=1s", "alpha", nil)
	if w.Code != 200 || w.Body.Len() != 12345 {
		t.Fatalf("status=%d bytes=%d", w.Code, w.Body.Len())
	}
	if !w.Flushed {
		t.Error("download buffered")
	}
	for k, v := range map[string]string{"Cache-Control": "no-store, no-cache, must-revalidate, no-transform", "Content-Type": "application/octet-stream", "X-Accel-Buffering": "no"} {
		if w.Header().Get(k) != v {
			t.Errorf("%s = %q", k, w.Header().Get(k))
		}
	}
	if w.Header().Get("Content-Encoding") != "" {
		t.Error("compressed response")
	}
	w = request(t, testServer(t, nil).Handler(), "GET", "/api/v1/download", "beta", nil)
	if w.Body.Len() != 128<<10 {
		t.Fatal(w.Body.Len())
	}
}

func TestTransferValidation(t *testing.T) {
	h := testServer(t, nil).Handler()
	for _, path := range []string{
		"/api/v1/download?duration=0s", "/api/v1/download?duration=61s", "/api/v1/download?duration=oops",
		"/api/v1/download?bytes=-1", "/api/v1/download?bytes=999999999", "/api/v1/download?bytes=1&bytes=2",
		"/api/v1/download?duration=1s&duration=2s", "/api/v1/download?x=1", "/api/v1/download?duration=%zz",
	} {
		if w := request(t, h, "GET", path, "alpha", nil); w.Code != 400 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if w := request(t, h, "POST", "/api/v1/download", "alpha", nil); w.Code != 405 {
		t.Fatal(w.Code)
	}
	if w := request(t, h, "GET", "/missing", "alpha", nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestUploadCountsReceivedBytes(t *testing.T) {
	w := request(t, testServer(t, nil).Handler(), "POST", "/api/v1/upload?duration=1s", "alpha", bytes.NewReader(bytes.Repeat([]byte{1}, 17001)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Bytes   int64   `json:"bytes"`
		Elapsed float64 `json:"elapsed_seconds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Bytes != 17001 || result.Elapsed <= 0 {
		t.Fatalf("%+v", result)
	}
}

func TestUploadRejectsOversizeAndCompression(t *testing.T) {
	h := testServer(t, func(c *Config) { c.MaxBytes = 100 }).Handler()
	w := request(t, h, "POST", "/api/v1/upload", "alpha", strings.NewReader(strings.Repeat("a", 101)))
	if w.Code != 413 {
		t.Fatal(w.Code, w.Body.String())
	}
	r := httptest.NewRequest("POST", "/api/v1/upload", strings.NewReader("gzip"))
	r.Header.Set("Authorization", "Bearer alpha")
	r.Header.Set("Content-Encoding", "gzip")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("POST", "/api/v1/upload", strings.NewReader(strings.Repeat("a", 101)))
	r.ContentLength = -1
	r.Header.Set("Authorization", "Bearer alpha")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatal("chunked cap bypass", w.Code)
	}
}

func TestAdmissionLimitsAndRelease(t *testing.T) {
	s := testServer(t, func(c *Config) { c.MaxConcurrent = 2; c.MaxPerToken = 1 })
	releaseA, ok := s.gate.acquire(0)
	if !ok {
		t.Fatal("first slot refused")
	}
	if _, ok := s.gate.acquire(0); ok {
		t.Fatal("per-token cap bypass")
	}
	releaseB, ok := s.gate.acquire(1)
	if !ok {
		t.Fatal("second token refused")
	}
	if w := request(t, s.Handler(), "GET", "/api/v1/download", "beta", nil); w.Code != 429 {
		t.Fatal(w.Code)
	}
	if w := request(t, s.Handler(), "GET", "/api/v1/ping", "alpha", nil); w.Code != 204 {
		t.Fatal("ping blocked by transfers")
	}
	releaseA()
	releaseB()
	if w := request(t, s.Handler(), "GET", "/api/v1/download?bytes=1", "alpha", nil); w.Code != 200 {
		t.Fatal("slot not released", w.Code)
	}
}

func TestStreamingOverRealHTTP(t *testing.T) {
	s := testServer(t, func(c *Config) { c.BytesPerSecond = 1 << 20; c.MaxBytes = 1 << 20; c.MaxPerToken = 1 })
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/download?duration=2s", nil)
	r.Header.Set("Authorization", "Bearer alpha")
	start := time.Now()
	resp, err := ts.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Error("headers held until transfer end")
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()
	deadline := time.Now().Add(time.Second)
	for {
		if release, ok := s.gate.acquire(0); ok {
			release()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled request retained slot")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSharedRateLimiterCancellation(t *testing.T) {
	l := newRateLimiter(100)
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := l.wait(ctx, 5); err == nil {
		t.Fatal("rate wait ignored deadline")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	if err := l.wait(ctx2, 5); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatal("rate limit bypass")
	}
}

type oneByteReader struct{ io.Reader }

func (r oneByteReader) Read(p []byte) (int, error) { return r.Reader.Read(p[:1]) }

func TestUploadRateChargesActualBytes(t *testing.T) {
	h := testServer(t, func(c *Config) { c.BytesPerSecond = 1000 }).Handler()
	start := time.Now()
	w := request(t, h, "POST", "/api/v1/upload?duration=1s", "alpha", oneByteReader{strings.NewReader(strings.Repeat("a", 20))})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Fatal("charged full buffer for short reads")
	}
}

func TestUploadReadDeadlineOnSlowPeer(t *testing.T) {
	s := testServer(t, nil)
	ts := httptest.NewUnstartedServer(s.Handler())
	ts.Config = s.HTTPServer("")
	ts.Start()
	defer ts.Close()
	conn, err := net.DialTimeout("tcp", ts.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	_, err = io.WriteString(conn, "POST /api/v1/upload?duration=50ms HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer alpha\r\nContent-Length: 100\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 408 {
		t.Fatal("slow body not timed out", resp.StatusCode)
	}
	if release, ok := s.gate.acquire(0); !ok {
		t.Fatal("timed out upload retained slot")
	} else {
		release()
	}
}

func TestDownloadDurationAbortsStream(t *testing.T) {
	ts := httptest.NewServer(testServer(t, func(c *Config) { c.BytesPerSecond = 1000 }).Handler())
	defer ts.Close()
	r, _ := http.NewRequest("GET", ts.URL+"/api/v1/download?duration=30ms", nil)
	r.Header.Set("Authorization", "Bearer alpha")
	resp, err := ts.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("timed-out stream ended with clean EOF")
	}
}

func TestRejectedUploadClosesWithoutDraining(t *testing.T) {
	for _, tc := range []struct {
		name, token, length string
		status              int
	}{
		{"unauthorized", "", "1", 401},
		{"oversize", "alpha", "100", 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t, func(c *Config) { c.MaxBytes = 10; c.MaxDuration = time.Second })
			ts := httptest.NewUnstartedServer(s.Handler())
			ts.Config = s.HTTPServer("")
			ts.Start()
			defer ts.Close()
			conn, err := net.DialTimeout("tcp", ts.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
			auth := ""
			if tc.token != "" {
				auth = "Authorization: Bearer " + tc.token + "\r\n"
			}
			_, err = io.WriteString(conn, "POST /api/v1/upload HTTP/1.1\r\nHost: localhost\r\n"+auth+"Content-Length: "+tc.length+"\r\n\r\n")
			if err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(conn)
			resp, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.status {
				t.Fatal(resp.StatusCode)
			}
			if _, err := io.ReadAll(resp.Body); err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
				t.Fatalf("rejected upload retained socket while draining body: %v", err)
			}
		})
	}
}
