// Package client measures application payload throughput and HTTP RTT.
package client

import (
	"context"
	"crypto/tls"
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
	ServerURL       string
	Token           string
	Connections     int
	Duration        time.Duration
	Timeout         time.Duration
	LatencySamples  int
	LatencyInterval time.Duration
	IPVersion       string
	OnPhase         func(string)
}

func DefaultConfig() Config {
	return Config{Connections: 4, Duration: 10 * time.Second, Timeout: 5 * time.Second, LatencySamples: 20, LatencyInterval: 50 * time.Millisecond, IPVersion: "auto"}
}

func (c Config) Validate() error {
	u, err := url.Parse(c.ServerURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("server must be an http(s) URL without credentials, query or fragment")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return errors.New("invalid server port")
		}
	}
	if c.Token == "" || len(c.Token) > 4096 {
		return errors.New("a bearer token of 1 to 4096 characters is required")
	}
	for _, b := range []byte(c.Token) {
		if b <= ' ' || b > '~' || b == ',' {
			return errors.New("token cannot contain whitespace, commas or non-ASCII characters")
		}
	}
	if c.Connections < 1 || c.Connections > 256 {
		return errors.New("connections must be between 1 and 256")
	}
	if c.Duration < 100*time.Millisecond || c.Duration > 24*time.Hour {
		return errors.New("duration must be between 100ms and 24h")
	}
	if c.Timeout <= 0 || c.Timeout > time.Minute {
		return errors.New("timeout must be in (0, 1m]")
	}
	if c.LatencySamples < 1 || c.LatencySamples > 10000 {
		return errors.New("latency-samples must be between 1 and 10000")
	}
	if c.LatencyInterval < 0 || c.LatencyInterval > time.Minute {
		return errors.New("latency-interval must be between 0 and 1m")
	}
	if c.IPVersion != "auto" && c.IPVersion != "4" && c.IPVersion != "6" {
		return errors.New("ip must be auto, 4 or 6")
	}
	return nil
}

type runner struct {
	config Config
	base   string
	info   protocol.Info
}

func (r *runner) httpClient() *http.Client {
	dialer := &net.Dialer{Timeout: r.config.Timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if r.config.IPVersion != "auto" {
				network = "tcp" + r.config.IPVersion
			}
			return dialer.DialContext(ctx, network, address)
		},
		DisableCompression: true, ForceAttemptHTTP2: false,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}},
		TLSNextProto:    make(map[string]func(string, *tls.Conn) http.RoundTripper),
		MaxIdleConns:    1, MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1, IdleConnTimeout: 30 * time.Second,
		TLSHandshakeTimeout: r.config.Timeout, ResponseHeaderTimeout: r.config.Timeout, MaxResponseHeaderBytes: 16 << 10,
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (r *runner) request(ctx context.Context, h *http.Client, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.base+protocol.Prefix+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.config.Token)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-cache, no-store, no-transform")
	req.Header.Set("User-Agent", "SpeedProbe/1")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = -1
	}
	resp, err := h.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, strings.Split(path, "?")[0], err)
	}
	want := 200
	if path == "/ping" {
		want = 204
	}
	if resp.StatusCode != want {
		defer resp.Body.Close()
		var detail protocol.Error
		_ = decodeResponse(resp, &detail)
		return nil, fmt.Errorf("%s %s: HTTP %d %s", method, strings.Split(path, "?")[0], resp.StatusCode, detail.Error)
	}
	if err := checkResponse(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

func checkResponse(resp *http.Response) error {
	encoding := resp.Header.Get("Content-Encoding")
	if resp.Uncompressed || (encoding != "" && !strings.EqualFold(encoding, "identity")) {
		return errors.New("compressed response would invalidate measurement")
	}
	if age := resp.Header.Get("Age"); age != "" && age != "0" {
		return errors.New("cached response would invalidate measurement")
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("X-Cache")), "hit") {
		return errors.New("cached response would invalidate measurement")
	}
	if resp.Header.Get("X-Speedprobe-Protocol") != protocol.Version {
		return errors.New("missing or incompatible SpeedProbe protocol header")
	}
	cache := strings.ToLower(resp.Header.Get("Cache-Control"))
	if !strings.Contains(cache, "no-store") || !strings.Contains(cache, "no-transform") {
		return errors.New("missing anti-cache/transform response headers")
	}
	return nil
}

func decodeResponse(resp *http.Response, v any) error {
	const maxSize = 64 << 10
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return err
	}
	if len(b) > maxSize {
		return errors.New("server JSON response too large")
	}
	return json.Unmarshal(b, v)
}

func (r *runner) ping(ctx context.Context, h *http.Client) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()
	start := time.Now()
	resp, err := r.request(ctx, h, "GET", "/ping", nil)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return float64(time.Since(start)) / float64(time.Millisecond), nil
}

func Run(ctx context.Context, c Config) (Result, error) {
	if err := c.Validate(); err != nil {
		return Result{}, err
	}
	r := &runner{config: c, base: strings.TrimRight(c.ServerURL, "/")}
	result := Result{SchemaVersion: 1, Server: r.base, StartedAt: time.Now().UTC(), Connections: c.Connections, RequestedDurationSeconds: c.Duration.Seconds()}
	control := r.httpClient()
	defer control.CloseIdleConnections()
	infoCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	resp, err := r.request(infoCtx, control, "GET", "/info", nil)
	if err == nil {
		err = decodeResponse(resp, &r.info)
		resp.Body.Close()
	}
	cancel()
	if err != nil {
		return Result{}, fmt.Errorf("server preflight: %w", err)
	}
	if r.info.ProtocolVersion != protocol.Version || r.info.MaxDurationSeconds <= 0 || r.info.MaxDurationSeconds > 86400 || r.info.MaxBytes <= 0 || r.info.BytesPerSecond < 0 || r.info.MaxConcurrent < 1 || r.info.MaxPerToken < 1 {
		return Result{}, errors.New("invalid server capabilities")
	}
	if c.Duration.Seconds() > r.info.MaxDurationSeconds {
		return Result{}, fmt.Errorf("duration exceeds server maximum %.3fs", r.info.MaxDurationSeconds)
	}
	if c.Connections > r.info.MaxConcurrent || c.Connections > r.info.MaxPerToken {
		return Result{}, errors.New("connections exceed server concurrency limit")
	}
	result.ServerLimits = r.info
	phase := func(name string) {
		if c.OnPhase != nil {
			c.OnPhase(name)
		}
	}
	phase("Measuring latency")
	if _, err := r.ping(ctx, control); err != nil {
		return Result{}, fmt.Errorf("latency warmup: %w", err)
	}
	samples := make([]float64, 0, c.LatencySamples)
	for i := 0; i < c.LatencySamples; i++ {
		ms, err := r.ping(ctx, control)
		if err != nil {
			return Result{}, fmt.Errorf("latency sample %d: %w", i+1, err)
		}
		samples = append(samples, ms)
		if i+1 < c.LatencySamples && c.LatencyInterval > 0 {
			timer := time.NewTimer(c.LatencyInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return Result{}, ctx.Err()
			case <-timer.C:
			}
		}
	}
	result.Latency = latencyStats(samples)
	phase("Measuring download")
	result.Download, err = r.measure(ctx, false)
	if err != nil {
		return Result{}, fmt.Errorf("download: %w", err)
	}
	phase("Measuring upload")
	result.Upload, err = r.measure(ctx, true)
	if err != nil {
		return Result{}, fmt.Errorf("upload: %w", err)
	}
	return result, nil
}
