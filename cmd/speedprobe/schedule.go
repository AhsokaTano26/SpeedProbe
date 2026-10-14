package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"speedprobe/internal/client"
)

// testRecord is the append-only log format, also used for periodic JSON stdout.
// A failed test has no result: partial measurements must not look successful.
type testRecord struct {
	SchemaVersion int            `json:"schema_version"`
	Run           int            `json:"run"`
	Server        string         `json:"server"`
	StartedAt     time.Time      `json:"started_at"`
	CompletedAt   time.Time      `json:"completed_at"`
	Status        string         `json:"status"`
	Error         string         `json:"error,omitempty"`
	Result        *client.Result `json:"result,omitempty"`
}

func openTestLog(path string) (*os.File, error) {
	if stat, err := os.Stat(path); err == nil {
		if !stat.Mode().IsRegular() {
			return nil, errors.New("log-file must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	stat, err := f.Stat()
	if err == nil && stat.Size() > 0 {
		// A final newline is optional in JSON Lines. Preserve that existing
		// record's boundary before appending, reading only one byte.
		var last [1]byte
		_, err = f.ReadAt(last[:], stat.Size()-1)
		if err == nil && last[0] != '\n' {
			_, err = f.Write([]byte("\n"))
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func redactError(err error, token string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if token != "" {
		redacted := strings.ReplaceAll(message, token, "[redacted]")
		if redacted != message {
			return errors.New(redacted)
		}
	}
	return err
}

// runTests serializes measurements. Timers are based on actual start times,
// so a slow test neither overlaps another test nor queues catch-up work.
func runTests(ctx context.Context, c testConfig, out, errOut io.Writer, measure func(context.Context, client.Config) (client.Result, error)) (returnErr error) {
	defer func() { returnErr = redactError(returnErr, c.Client.Token) }()
	periodic := c.Interval > 0
	if err := ctx.Err(); err != nil {
		if periodic {
			return nil
		}
		return err
	}
	var logFile *os.File
	if c.LogFile != "" {
		var err error
		logFile, err = openTestLog(c.LogFile)
		if err != nil {
			return fmt.Errorf("open test log: %w", err)
		}
		defer func() {
			if err := logFile.Close(); err != nil && returnErr == nil {
				returnErr = fmt.Errorf("close test log: %w", err)
			}
		}()
	}
	if !c.JSON {
		c.Client.OnPhase = func(phase string) { fmt.Fprintln(errOut, phase+"...") }
		if periodic {
			fmt.Fprintf(errOut, "Testing every %s; logging to %s. Press Ctrl+C to stop.\n", c.Interval, c.LogFile)
		}
	}
	var failed int
	var lastFailure error
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			if periodic {
				return nil
			}
			return err
		}
		started := time.Now()
		if periodic && !c.JSON {
			fmt.Fprintf(errOut, "\n[%s] Test #%d\n", started.Format(time.RFC3339), attempt)
		}
		result, err := measure(ctx, c.Client)
		err = redactError(err, c.Client.Token)
		record := testRecord{SchemaVersion: 1, Run: attempt, Server: c.Client.ServerURL, StartedAt: started.UTC(), CompletedAt: time.Now().UTC(), Status: "success"}
		if err != nil {
			record.Status = "error"
			record.Error = err.Error()
			failed++
			lastFailure = err
			if ctx.Err() != nil {
				record.Status = "canceled"
			}
		} else {
			record.Result = &result
		}
		if logFile != nil {
			// Encode writes each complete line directly; Sync commits each round
			// before waiting for another, without retaining a history in memory.
			if err := json.NewEncoder(logFile).Encode(record); err != nil {
				return fmt.Errorf("write test log: %w", err)
			}
			if err := logFile.Sync(); err != nil {
				return fmt.Errorf("sync test log: %w", err)
			}
		}
		if c.JSON && periodic {
			if err := json.NewEncoder(out).Encode(record); err != nil {
				return fmt.Errorf("write JSON result: %w", err)
			}
		} else if err == nil {
			if c.JSON {
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(result); err != nil {
					return err
				}
			} else if err := result.WriteText(out); err != nil {
				return err
			}
		}
		if !periodic {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			fmt.Fprintf(errOut, "[%s] Test #%d failed: %v\n", record.CompletedAt.Format(time.RFC3339), attempt, err)
		}
		if c.Count > 0 && attempt >= c.Count {
			if failed > 0 {
				return fmt.Errorf("%d of %d scheduled tests failed (last error: %w)", failed, attempt, lastFailure)
			}
			return nil
		}
		if delay := time.Until(started.Add(c.Interval)); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
	}
}
