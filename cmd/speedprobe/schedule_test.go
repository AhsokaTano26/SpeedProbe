package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"speedprobe/internal/client"
)

func scheduledConfig(t *testing.T) testConfig {
	t.Helper()
	c := testConfig{Client: client.DefaultConfig(), Interval: 20 * time.Millisecond, Count: 2, JSON: true, LogFile: filepath.Join(t.TempDir(), "logs", "speedprobe.jsonl")}
	c.Client.ServerURL = "http://localhost:8080"
	c.Client.Token = "private-secret"
	return c
}

func sampleResult(c client.Config) client.Result {
	return client.Result{SchemaVersion: 1, Server: c.ServerURL, StartedAt: time.Now().UTC(), Connections: c.Connections,
		Download: client.Transfer{Bytes: 1234, Mbps: 5}, Upload: client.Transfer{Bytes: 567, Mbps: 2}}
}

func readRecords(t *testing.T, path string) []testRecord {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var records []testRecord
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var record testRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func TestPeriodicResultsAreAppendedImmediately(t *testing.T) {
	c := scheduledConfig(t)
	var out, errOut bytes.Buffer
	measure := func(ctx context.Context, cfg client.Config) (client.Result, error) { return sampleResult(cfg), nil }
	for range 2 {
		if err := runTests(context.Background(), c, &out, &errOut, measure); err != nil {
			t.Fatal(err)
		}
	}
	records := readRecords(t, c.LogFile)
	if len(records) != 4 {
		t.Fatalf("got %d records", len(records))
	}
	for _, record := range records {
		if record.Status != "success" || record.Result == nil || record.Result.Download.Bytes != 1234 || record.StartedAt.IsZero() || record.CompletedAt.Before(record.StartedAt) {
			t.Fatalf("%+v", record)
		}
	}
	if strings.Contains(out.String(), c.Client.Token) {
		t.Fatal("token exposed")
	}
	if len(bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))) != 4 {
		t.Fatal("periodic stdout is not JSON Lines")
	}
	stat, err := os.Stat(c.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && stat.Mode().Perm()&0077 != 0 {
		t.Fatal("log readable by other users")
	}
}

func TestPeriodicFailureIsLoggedAndNextRunContinues(t *testing.T) {
	c := scheduledConfig(t)
	var out, errOut bytes.Buffer
	invocations := 0
	err := runTests(context.Background(), c, &out, &errOut, func(ctx context.Context, cfg client.Config) (client.Result, error) {
		invocations++
		if invocations == 1 {
			return client.Result{}, errors.New("connection failed; token=" + cfg.Token)
		}
		return sampleResult(cfg), nil
	})
	if err == nil {
		t.Fatal("finite batch hid failure")
	}
	records := readRecords(t, c.LogFile)
	if len(records) != 2 || records[0].Status != "error" || records[0].Error == "" || records[0].Result != nil || records[1].Status != "success" {
		t.Fatalf("%+v", records)
	}
	b, _ := os.ReadFile(c.LogFile)
	if strings.Contains(string(b)+out.String()+errOut.String()+err.Error(), c.Client.Token) {
		t.Fatal("token leaked from error")
	}
}

func TestPeriodicCancellationStopsWaitPromptly(t *testing.T) {
	c := scheduledConfig(t)
	c.Interval = time.Hour
	c.Count = 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	measured := make(chan struct{})
	go func() {
		done <- runTests(ctx, c, io.Discard, io.Discard, func(ctx context.Context, cfg client.Config) (client.Result, error) {
			close(measured)
			return sampleResult(cfg), nil
		})
	}()
	<-measured
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("scheduler ignored cancellation")
	}
	if records := readRecords(t, c.LogFile); len(records) != 1 {
		t.Fatal("canceled wait started another test")
	}
}

func TestPeriodicCancellationLogsInterruptedRun(t *testing.T) {
	c := scheduledConfig(t)
	c.Count = 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := runTests(ctx, c, io.Discard, io.Discard, func(ctx context.Context, cfg client.Config) (client.Result, error) {
		cancel()
		return client.Result{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	records := readRecords(t, c.LogFile)
	if len(records) != 1 || records[0].Status != "canceled" {
		t.Fatalf("%+v", records)
	}
}

func TestSingleJSONOutputRemainsCompatible(t *testing.T) {
	c := scheduledConfig(t)
	c.Interval = 0
	c.Count = 0
	var out bytes.Buffer
	if err := runTests(context.Background(), c, &out, io.Discard, func(ctx context.Context, cfg client.Config) (client.Result, error) { return sampleResult(cfg), nil }); err != nil {
		t.Fatal(err)
	}
	var result client.Result
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Download.Bytes != 1234 {
		t.Fatalf("old JSON schema changed: %s %v", out.String(), err)
	}
	if len(readRecords(t, c.LogFile)) != 1 {
		t.Fatal("single result was not logged")
	}
}

func TestLogOpenFailurePreventsMeasurement(t *testing.T) {
	c := scheduledConfig(t)
	c.LogFile = t.TempDir()
	err := runTests(context.Background(), c, io.Discard, io.Discard, func(ctx context.Context, cfg client.Config) (client.Result, error) {
		t.Fatal("measurement started without writable log")
		return client.Result{}, nil
	})
	if err == nil {
		t.Fatal("invalid log path accepted")
	}
}

func TestLongTestNeverOverlapsOrCatchesUp(t *testing.T) {
	c := scheduledConfig(t)
	c.Interval = 5 * time.Millisecond
	c.Count = 3
	if err := runTests(context.Background(), c, io.Discard, io.Discard, func(ctx context.Context, cfg client.Config) (client.Result, error) {
		time.Sleep(15 * time.Millisecond)
		return sampleResult(cfg), nil
	}); err != nil {
		t.Fatal(err)
	}
	records := readRecords(t, c.LogFile)
	if len(records) != 3 {
		t.Fatal(len(records))
	}
	for i := 1; i < len(records); i++ {
		if records[i].StartedAt.Before(records[i-1].CompletedAt) {
			t.Fatal("tests overlapped")
		}
	}
}

func TestAppendPreservesRecordWithoutTrailingNewline(t *testing.T) {
	c := scheduledConfig(t)
	c.Count = 1
	c.LogFile = filepath.Join(t.TempDir(), "existing.jsonl")
	if err := os.WriteFile(c.LogFile, []byte(`{"schema_version":1,"run":99,"status":"success"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runTests(context.Background(), c, io.Discard, io.Discard, func(ctx context.Context, cfg client.Config) (client.Result, error) { return sampleResult(cfg), nil }); err != nil {
		t.Fatal(err)
	}
	records := readRecords(t, c.LogFile)
	if len(records) != 2 || records[0].Run != 99 || records[1].Run != 1 {
		t.Fatalf("%+v", records)
	}
}

func TestLogSetupErrorRedactsTokenInPath(t *testing.T) {
	c := scheduledConfig(t)
	parent := filepath.Join(t.TempDir(), c.Client.Token)
	if err := os.WriteFile(parent, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	c.LogFile = filepath.Join(parent, "results.jsonl")
	err := runTests(context.Background(), c, io.Discard, io.Discard, func(ctx context.Context, cfg client.Config) (client.Result, error) {
		t.Fatal("measurement ran with invalid log")
		return client.Result{}, nil
	})
	if err == nil || strings.Contains(err.Error(), c.Client.Token) {
		t.Fatalf("unsafe log error: %v", err)
	}
}
