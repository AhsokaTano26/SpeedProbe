package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"speedprobe/internal/client"
	"speedprobe/internal/server"
)

func TestJSONCommandOutput(t *testing.T) {
	c := server.DefaultConfig()
	c.Tokens = []string{"alpha"}
	c.BytesPerSecond = 4 << 20
	s, err := server.New(c)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	var out, errOut bytes.Buffer
	err = run(context.Background(), []string{"test", "--server", ts.URL, "--connections", "2", "--duration", "200ms", "--timeout", "1s", "--latency-samples", "2", "--latency-interval", "0", "--json"}, &out, &errOut, env(map[string]string{"SPEEDPROBE_TOKEN": "alpha"}))
	if err != nil {
		t.Fatal(err)
	}
	var r client.Result
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if errOut.Len() != 0 || r.Download.Bytes <= 0 || r.Upload.Bytes <= 0 {
		t.Fatalf("bad command output: %s", errOut.String())
	}
}

func TestServeCancellation(t *testing.T) {
	c := serveConfig{Addr: "127.0.0.1:0", Network: "tcp", Server: server.DefaultConfig()}
	c.Server.Tokens = []string{"alpha"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, c, io.Discard) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestPeriodicCommandAgainstRealServer(t *testing.T) {
	c := server.DefaultConfig()
	c.Tokens = []string{"alpha"}
	c.BytesPerSecond = 4 << 20
	s, err := server.New(c)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	logPath := filepath.Join(t.TempDir(), "results.jsonl")
	var out, errOut bytes.Buffer
	err = run(context.Background(), []string{"test", "--server", ts.URL, "--connections", "2", "--duration", "200ms", "--timeout", "1s", "--latency-samples", "2", "--latency-interval", "0", "--interval", "500ms", "--count", "2", "--log-file", logPath, "--json"}, &out, &errOut, env(map[string]string{"SPEEDPROBE_TOKEN": "alpha"}))
	if err != nil {
		t.Fatal(err)
	}
	if errOut.Len() != 0 {
		t.Fatal(errOut.String())
	}
	records := readRecords(t, logPath)
	if len(records) != 2 {
		t.Fatal(len(records))
	}
	for i, record := range records {
		if record.Run != i+1 || record.Status != "success" || record.Result == nil || record.Result.Upload.Bytes <= 0 || record.Result.Download.Bytes <= 0 {
			t.Fatalf("%+v", record)
		}
	}
	if records[1].StartedAt.Sub(records[0].StartedAt) < 450*time.Millisecond {
		t.Fatal("interval ignored")
	}
}
