// Package server implements authenticated, bounded streaming speed tests.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"speedprobe/internal/protocol"
)

type Config struct {
	Tokens         []string
	MaxConcurrent  int
	MaxPerToken    int
	MaxBytes       int64
	MaxDuration    time.Duration
	BytesPerSecond int64
}

func DefaultConfig() Config {
	return Config{MaxConcurrent: 32, MaxPerToken: 8, MaxBytes: 1 << 30, MaxDuration: 60 * time.Second}
}

type Server struct {
	config  Config
	tokens  [][sha256.Size]byte
	block   []byte
	gate    admission
	limiter *rateLimiter
}

func New(c Config) (*Server, error) {
	if len(c.Tokens) == 0 || len(c.Tokens) > 1024 {
		return nil, errors.New("configure between 1 and 1024 bearer tokens")
	}
	if c.MaxConcurrent < 1 || c.MaxConcurrent > 4096 || c.MaxPerToken < 1 || c.MaxPerToken > c.MaxConcurrent {
		return nil, errors.New("concurrency must satisfy 1 <= per-token <= global <= 4096")
	}
	if c.MaxBytes <= 0 || c.MaxBytes == int64(^uint64(0)>>1) {
		return nil, errors.New("max-bytes must be positive and below MaxInt64")
	}
	if c.MaxDuration <= 0 || c.MaxDuration > 24*time.Hour {
		return nil, errors.New("max-duration must be in (0, 24h]")
	}
	if c.BytesPerSecond < 0 {
		return nil, errors.New("bytes-per-second must be non-negative")
	}
	s := &Server{config: c, block: make([]byte, protocol.BlockSize), limiter: newRateLimiter(c.BytesPerSecond)}
	// Do not retain plaintext secrets after initialization.
	s.config.Tokens = nil
	seen := make(map[[sha256.Size]byte]bool, len(c.Tokens))
	for _, token := range c.Tokens {
		if len(token) == 0 || len(token) > 4096 {
			return nil, errors.New("tokens must contain 1 to 4096 printable ASCII characters")
		}
		for _, b := range []byte(token) {
			if b <= ' ' || b > '~' || b == ',' {
				return nil, errors.New("tokens cannot contain whitespace, commas or non-ASCII characters")
			}
		}
		digest := sha256.Sum256([]byte(token))
		if seen[digest] {
			return nil, errors.New("duplicate bearer token")
		}
		seen[digest] = true
		s.tokens = append(s.tokens, digest)
	}
	s.gate = admission{globalLimit: c.MaxConcurrent, tokenLimit: c.MaxPerToken, perToken: make([]int, len(s.tokens))}
	if _, err := rand.Read(s.block); err != nil {
		return nil, fmt.Errorf("create transfer payload: %w", err)
	}
	return s, nil
}

func (s *Server) HTTPServer(addr string) *http.Server {
	return &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: s.config.MaxDuration + 5*time.Second, WriteTimeout: s.config.MaxDuration + 5*time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
}

func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }

func headers(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, no-transform")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Speedprobe-Protocol", protocol.Version)
}

func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, message string) {
	// Connection: close does not stop net/http's final Body.Close from trying
	// to drain a small/chunked body. Expire reads so rejected slow uploads
	// release their socket promptly; response writes have a separate deadline.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now())
	w.Header().Set("Connection", "close")
	jsonResponse(w, status, protocol.Error{Error: message})
}

func (s *Server) authenticate(r *http.Request) (int, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return 0, false
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || len(token) == 0 || len(token) > 4096 {
		return 0, false
	}
	digest := sha256.Sum256([]byte(token))
	index := -1
	for i, expected := range s.tokens {
		if subtle.ConstantTimeCompare(digest[:], expected[:]) == 1 {
			index = i
		}
	}
	return index, index >= 0
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	headers(w)
	if r.URL.Path == "/healthz" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			fail(w, 405, "method not allowed")
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			fail(w, 400, "GET requests cannot have a body")
			return
		}
		jsonResponse(w, 200, map[string]string{"status": "ok"})
		return
	}
	token, ok := s.authenticate(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="SpeedProbe"`)
		fail(w, 401, "unauthorized")
		return
	}
	want := http.MethodGet
	if r.URL.Path == protocol.Prefix+"/upload" {
		want = http.MethodPost
	}
	switch r.URL.Path {
	case protocol.Prefix + "/info", protocol.Prefix + "/ping", protocol.Prefix + "/download", protocol.Prefix + "/upload":
	default:
		fail(w, 404, "endpoint not found")
		return
	}
	if r.Method != want {
		w.Header().Set("Allow", want)
		fail(w, 405, "method not allowed")
		return
	}
	if want == http.MethodGet && (r.ContentLength != 0 || len(r.TransferEncoding) > 0) {
		fail(w, 400, "GET requests cannot have a body")
		return
	}
	switch r.URL.Path {
	case protocol.Prefix + "/info":
		jsonResponse(w, 200, protocol.Info{ProtocolVersion: protocol.Version, MaxDurationSeconds: s.config.MaxDuration.Seconds(), MaxBytes: s.config.MaxBytes,
			MaxConcurrent: s.config.MaxConcurrent, MaxPerToken: s.config.MaxPerToken, BytesPerSecond: s.config.BytesPerSecond})
		return
	case protocol.Prefix + "/ping":
		w.WriteHeader(204)
		return
	}
	duration, byteLimit, err := s.transferParams(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if want == http.MethodPost {
		encoding := r.Header.Values("Content-Encoding")
		if len(encoding) > 1 || (len(encoding) == 1 && encoding[0] != "" && !strings.EqualFold(encoding[0], "identity")) {
			fail(w, 415, "compressed uploads are not supported")
			return
		}
		if r.ContentLength > byteLimit {
			fail(w, 413, "upload exceeds byte limit")
			return
		}
	}
	release, ok := s.gate.acquire(token)
	if !ok {
		w.Header().Set("Retry-After", "1")
		fail(w, 429, "transfer concurrency limit reached")
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), duration)
	defer cancel()
	if want == http.MethodGet {
		s.download(ctx, w, byteLimit, duration)
	} else {
		s.upload(ctx, w, r, byteLimit, duration)
	}
}

func (s *Server) transferParams(r *http.Request) (time.Duration, int64, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, 0, err
	}
	duration := s.config.MaxDuration
	byteLimit := s.config.MaxBytes
	for name, values := range q {
		if len(values) != 1 {
			return 0, 0, errors.New("duplicate query parameter")
		}
		switch name {
		case "duration":
			duration, err = time.ParseDuration(values[0])
			if err != nil || duration <= 0 || duration > s.config.MaxDuration {
				return 0, 0, errors.New("duration must be positive and within server limit")
			}
		case "bytes":
			if r.Method != http.MethodGet {
				return 0, 0, errors.New("bytes is only supported for downloads")
			}
			byteLimit, err = strconv.ParseInt(values[0], 10, 64)
			if err != nil || byteLimit <= 0 || byteLimit > s.config.MaxBytes {
				return 0, 0, errors.New("bytes must be positive and within server limit")
			}
		default:
			return 0, 0, errors.New("unknown query parameter")
		}
	}
	return duration, byteLimit, nil
}

func (s *Server) download(ctx context.Context, w http.ResponseWriter, byteLimit int64, duration time.Duration) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(duration))
	defer rc.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Speedprobe-Limit-Bytes", strconv.FormatInt(byteLimit, 10))
	w.WriteHeader(200)
	if err := rc.Flush(); err != nil {
		return
	}
	for sent := int64(0); sent < byteLimit; {
		n := int(min(int64(s.limiter.quantum), byteLimit-sent))
		if err := s.limiter.wait(ctx, n); err != nil {
			panic(http.ErrAbortHandler)
		}
		written, err := w.Write(s.block[:n])
		sent += int64(written)
		if err != nil || written != n {
			panic(http.ErrAbortHandler)
		}
		if err := rc.Flush(); err != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

func (s *Server) upload(ctx context.Context, w http.ResponseWriter, r *http.Request, byteLimit int64, duration time.Duration) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(duration))
	defer rc.SetReadDeadline(time.Time{})
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, byteLimit)
	buffer := make([]byte, s.limiter.quantum)
	started := time.Now()
	var received int64
	for {
		// Reserve at most the remaining limit + one detection byte.
		next := int(min(int64(len(buffer)), byteLimit-received+1))
		n, err := r.Body.Read(buffer[:next])
		// A short network read must spend only its actual payload bytes.
		// One bounded buffer may be read ahead while the shared bucket waits.
		if waitErr := s.limiter.wait(ctx, n); waitErr != nil {
			fail(w, 408, "upload duration limit reached")
			return
		}
		received += int64(n)
		if errors.Is(err, io.EOF) {
			if ctx.Err() != nil {
				fail(w, 408, "upload duration limit reached")
				return
			}
			jsonResponse(w, 200, protocol.UploadResult{Bytes: received, ElapsedSeconds: time.Since(started).Seconds()})
			return
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			var timeout net.Error
			switch {
			case errors.As(err, &tooLarge):
				fail(w, 413, "upload exceeds byte limit")
			case ctx.Err() != nil || (errors.As(err, &timeout) && timeout.Timeout()):
				fail(w, 408, "upload duration limit reached")
			default:
				fail(w, 400, "incomplete upload body")
			}
			return
		}
	}
}
