package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(values map[string]string) func(string) string {
	return func(k string) string { return values[k] }
}

func TestServeConfigEnvironmentAndFlags(t *testing.T) {
	c, err := parseServe([]string{"--addr", "127.0.0.1:9999", "--max-bytes", "12345"}, env(map[string]string{
		"SPEEDPROBE_ADDR": ":8080", "SPEEDPROBE_TOKENS": "alpha, beta", "SPEEDPROBE_MAX_BYTES": "100",
	}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "127.0.0.1:9999" || c.Server.MaxBytes != 12345 || len(c.Server.Tokens) != 2 {
		t.Fatalf("%+v", c)
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--tokens", "alpha", "--max-bytes", "0"}, {"--tokens", "alpha", "--network", "udp"},
		{"--tokens", "alpha", "--max-duration", "invalid"}, {"--tokens", "alpha", "--tls-cert", "cert.pem"},
		{"--tokens", "alpha", "--max-concurrent", "1", "--max-per-token", "2"},
		{"--tokens", "alpha", "unexpected"},
	} {
		if _, err := parseServe(args, env(nil), io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, err := parseTest([]string{"--server", "http://localhost", "--token", "a", "--duration", "wrong"}, env(nil), io.Discard); err == nil {
		t.Fatal("invalid duration")
	}
}

func TestTokenFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokens")
	if err := os.WriteFile(p, []byte("# clients\nalpha\nbeta\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := parseServe([]string{"--tokens-file", p}, env(nil), io.Discard)
	if err != nil || len(c.Server.Tokens) != 2 {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := parseServe([]string{"--tokens-file", p, "--tokens", "gamma"}, env(nil), io.Discard); err == nil {
		t.Fatal("ambiguous sources")
	}
	if _, err := parseTest([]string{"--server", "http://localhost", "--token-file", p}, env(nil), io.Discard); err == nil {
		t.Fatal("client accepted multi-token file")
	}
	if err := os.WriteFile(p, []byte("alpha\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tc, err := parseTest([]string{"--server", "http://localhost", "--token-file", p, "--json"}, env(nil), io.Discard)
	if err != nil || tc.Client.Token != "alpha" || !tc.JSON {
		t.Fatalf("%+v %v", tc, err)
	}
}

func TestHelpAndUnknownCommand(t *testing.T) {
	for _, command := range []string{"serve", "test"} {
		var out, errOut bytes.Buffer
		if err := run(context.Background(), []string{command, "--help"}, &out, &errOut, env(nil)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(errOut.String(), "Usage:") {
			t.Fatal("missing help")
		}
	}
	if err := run(context.Background(), []string{"missing"}, io.Discard, io.Discard, env(nil)); err == nil {
		t.Fatal("unknown command accepted")
	}
	if _, err := parseServe([]string{"--help"}, env(nil), io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
}

func TestClientEnvironmentOverride(t *testing.T) {
	c, err := parseTest([]string{"--connections", "2", "--ip", "6"}, env(map[string]string{
		"SPEEDPROBE_SERVER": "http://[::1]:8080", "SPEEDPROBE_TOKEN": "alpha", "SPEEDPROBE_CONNECTIONS": "4",
		"SPEEDPROBE_DURATION": "2s", "SPEEDPROBE_JSON": "true",
	}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Client.Connections != 2 || c.Client.IPVersion != "6" || !c.JSON || c.Client.Duration.Seconds() != 2 {
		t.Fatalf("%+v", c)
	}
}

func TestHelpDoesNotExposeEnvironmentTokens(t *testing.T) {
	for _, command := range []string{"serve", "test"} {
		var out, errOut bytes.Buffer
		err := run(context.Background(), []string{command, "--help"}, &out, &errOut, env(map[string]string{"SPEEDPROBE_TOKEN": "private-secret", "SPEEDPROBE_TOKENS": "private-secret"}))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String()+errOut.String(), "private-secret") {
			t.Fatal("help exposed bearer token")
		}
	}
}

func TestPeriodicConfig(t *testing.T) {
	c, err := parseTest([]string{"--interval", "5m", "--count", "3", "--log-file", "logs/speedprobe.jsonl"}, env(map[string]string{
		"SPEEDPROBE_SERVER": "http://localhost:8080", "SPEEDPROBE_TOKEN": "alpha",
		"SPEEDPROBE_INTERVAL": "1h", "SPEEDPROBE_COUNT": "8", "SPEEDPROBE_LOG_FILE": "other.jsonl",
	}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Interval != 5*time.Minute || c.Count != 3 || c.LogFile != "logs/speedprobe.jsonl" {
		t.Fatalf("%+v", c)
	}
	c, err = parseTest([]string{"--interval", "1m"}, env(map[string]string{"SPEEDPROBE_SERVER": "http://localhost", "SPEEDPROBE_TOKEN": "alpha"}), io.Discard)
	if err != nil || c.LogFile != "speedprobe.jsonl" || c.Count != 0 {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = parseTest(nil, env(map[string]string{"SPEEDPROBE_SERVER": "http://localhost", "SPEEDPROBE_TOKEN": "alpha"}), io.Discard)
	if err != nil || c.Interval != 0 || c.Count != 0 || c.LogFile != "" {
		t.Fatalf("single-run defaults changed: %+v %v", c, err)
	}
}

func TestInvalidPeriodicConfig(t *testing.T) {
	for _, args := range [][]string{
		{"--interval", "-1s"}, {"--interval", "invalid"}, {"--count", "-1"}, {"--count", "2"}, {"--count", "invalid"},
	} {
		if _, err := parseTest(args, env(map[string]string{"SPEEDPROBE_SERVER": "http://localhost", "SPEEDPROBE_TOKEN": "alpha"}), io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
