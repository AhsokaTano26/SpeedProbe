package client

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"speedprobe/internal/server"
)

func TestTimedReaderPacesAdvertisedLimit(t *testing.T) {
	start := time.Now()
	r := &timedReader{ctx: context.Background(), end: start.Add(120 * time.Millisecond), block: make([]byte, 64<<10), rate: 1 << 20, next: start}
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		t.Fatal(err)
	}
	if float64(n) > float64(1<<20)*0.12 || time.Since(start) < 100*time.Millisecond {
		t.Fatal("upload exceeded advertised capacity", n)
	}
}

func localConfig(url string) Config {
	c := DefaultConfig()
	c.ServerURL = url
	c.Token = "alpha"
	c.Connections = 2
	c.Duration = 120 * time.Millisecond
	c.Timeout = time.Second
	c.LatencySamples = 3
	c.LatencyInterval = time.Millisecond
	return c
}

func localServer(t *testing.T, mutate func(*server.Config)) *httptest.Server {
	t.Helper()
	c := server.DefaultConfig()
	c.Tokens = []string{"alpha"}
	c.MaxBytes = 64 << 20
	c.BytesPerSecond = 4 << 20
	if mutate != nil {
		mutate(&c)
	}
	s, err := server.New(c)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestLatencyStatistics(t *testing.T) {
	l := latencyStats([]float64{1, 3, 2, 8})
	if l.MeanMS != 3.5 || l.MinMS != 1 || l.MaxMS != 8 || l.P50MS != 2.5 || l.P95MS != 8 {
		t.Fatalf("%+v", l)
	}
	if math.Abs(l.JitterMS-3) > 1e-9 {
		t.Fatal(l.JitterMS)
	}
	if latencyStats([]float64{2}).JitterMS != 0 {
		t.Fatal("single sample has jitter")
	}
}

func TestConfigValidation(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.ServerURL = "ftp://localhost" },
		func(c *Config) { c.ServerURL = "http://user:secret@localhost" },
		func(c *Config) { c.ServerURL = "http://localhost/?token=a" },
		func(c *Config) { c.ServerURL = "http://localhost/#x" },
		func(c *Config) { c.Token = "" },
		func(c *Config) { c.Token = "a\nb" },
		func(c *Config) { c.Connections = 0 },
		func(c *Config) { c.Duration = 0 },
		func(c *Config) { c.Timeout = 0 },
		func(c *Config) { c.LatencySamples = 0 },
		func(c *Config) { c.LatencyInterval = -1 },
		func(c *Config) { c.IPVersion = "7" },
	} {
		c := localConfig("http://localhost")
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Error("invalid config accepted")
		}
	}
	c := localConfig("http://[::1]:8080/prefix/")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRunAgainstRealServer(t *testing.T) {
	ts := localServer(t, nil)
	r, err := Run(context.Background(), localConfig(ts.URL))
	if err != nil {
		t.Fatal(err)
	}
	if r.Connections != 2 || r.Download.Bytes <= 0 || r.Upload.Bytes <= 0 {
		t.Fatalf("%+v", r)
	}
	if r.Download.ElapsedSeconds < 0.1 || r.Upload.ElapsedSeconds < 0.1 {
		t.Fatal("wrong window")
	}
	for _, transfer := range []Transfer{r.Download, r.Upload} {
		if math.Abs(transfer.Mbps-float64(transfer.Bytes)*8/transfer.ElapsedSeconds/1e6) > 1e-8 {
			t.Fatal("Mbps not based on bytes and elapsed time")
		}
	}
	if len(r.Latency.SamplesMS) != 3 {
		t.Fatal("missing RTT samples")
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "alpha") {
		t.Fatal("token in result")
	}
}

func TestAuthenticationAndAdvertisedLimits(t *testing.T) {
	ts := localServer(t, func(c *server.Config) { c.MaxPerToken = 1; c.MaxDuration = 200 * time.Millisecond })
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Token = "wrong" },
		func(c *Config) { c.Connections = 2 },
		func(c *Config) { c.Connections = 1; c.Duration = time.Second },
	} {
		c := localConfig(ts.URL)
		mutate(&c)
		if _, err := Run(context.Background(), c); err == nil {
			t.Error("auth/limit ignored")
		}
	}
}

func TestCancellation(t *testing.T) {
	ts := localServer(t, nil)
	c := localConfig(ts.URL)
	c.Duration = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Run(ctx, c); err == nil {
		t.Fatal("canceled run succeeded")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("cancellation did not stop workers")
	}
}

func TestEarlyDownloadEOFIsError(t *testing.T) {
	ts := localServer(t, func(c *server.Config) { c.MaxBytes = 1 })
	if _, err := Run(context.Background(), localConfig(ts.URL)); err == nil || !strings.Contains(err.Error(), "early") {
		t.Fatalf("got %v", err)
	}
}

func TestRefusesRedirects(t *testing.T) {
	var forwarded atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Store(true) }))
	defer target.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer ts.Close()
	if _, err := Run(context.Background(), localConfig(ts.URL)); err == nil {
		t.Fatal("redirect accepted")
	}
	if forwarded.Load() {
		t.Fatal("redirect followed")
	}
}

func TestRefusesCompressedAndCachedResponses(t *testing.T) {
	for _, header := range []string{"Content-Encoding", "Age"} {
		t.Run(header, func(t *testing.T) {
			base := localServer(t, nil)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if header == "Age" {
					w.Header().Set(header, "1")
				} else {
					w.Header().Set(header, "gzip")
				}
				base.Config.Handler.ServeHTTP(w, r)
			}))
			defer ts.Close()
			if _, err := Run(context.Background(), localConfig(ts.URL)); err == nil {
				t.Fatal("altered response accepted")
			}
		})
	}
}

func TestPathPrefixedProxy(t *testing.T) {
	base := localServer(t, nil)
	ts := httptest.NewServer(http.StripPrefix("/speed", base.Config.Handler))
	defer ts.Close()
	if _, err := Run(context.Background(), localConfig(ts.URL+"/speed/")); err != nil {
		t.Fatal(err)
	}
}

func TestUploadUsesAcknowledgementAndDrainTime(t *testing.T) {
	base := localServer(t, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/upload" {
			// Deliberately delay the response; counting bytes handed to the local
			// socket over the production window would overstate this path.
			w2 := httptest.NewRecorder()
			base.Config.Handler.ServeHTTP(w2, r)
			time.Sleep(80 * time.Millisecond)
			for k, v := range w2.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(w2.Code)
			w.Write(w2.Body.Bytes())
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer ts.Close()
	r, err := Run(context.Background(), localConfig(ts.URL))
	if err != nil {
		t.Fatal(err)
	}
	if r.Upload.ElapsedSeconds < 0.19 {
		t.Fatal("upload acknowledgement/drain excluded", r.Upload.ElapsedSeconds)
	}
}
