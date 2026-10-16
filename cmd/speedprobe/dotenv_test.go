package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"speedprobe/internal/client"
	"speedprobe/internal/server"
)

// Keep optional .env discovery independent of the developer's private files.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "speedprobe-command-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Chdir(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	// Leave the directory before removing it (required on Windows).
	os.Chdir(filepath.Dir(dir))
	os.RemoveAll(dir)
	os.Exit(code)
}

func dotenvDirectory(t *testing.T, contents string) string {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func TestClientAutomaticallyLoadsDotEnv(t *testing.T) {
	dotenvDirectory(t, "SPEEDPROBE_SERVER=http://localhost:8080\nSPEEDPROBE_TOKEN=file-secret\nSPEEDPROBE_CONNECTIONS=2\nSPEEDPROBE_DURATION=200ms\nSPEEDPROBE_INTERVAL=5m\nSPEEDPROBE_COUNT=3\nSPEEDPROBE_JSON=true\n")
	t.Setenv("SPEEDPROBE_TOKEN", "process-unchanged")
	c, err := parseTest(nil, env(nil), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Client.ServerURL != "http://localhost:8080" || c.Client.Token != "file-secret" || c.Client.Connections != 2 || c.Client.Duration != 200*time.Millisecond || c.Interval != 5*time.Minute || c.Count != 3 || !c.JSON {
		t.Fatalf("unexpected loaded configuration: %+v", c)
	}
	if os.Getenv("SPEEDPROBE_TOKEN") != "process-unchanged" {
		t.Fatal("loading .env mutated the process environment")
	}
}

func TestDotEnvConfigurationPrecedence(t *testing.T) {
	dotenvDirectory(t, "SPEEDPROBE_SERVER=http://file.invalid\nSPEEDPROBE_TOKEN=file-token\nSPEEDPROBE_CONNECTIONS=2\nSPEEDPROBE_JSON=true\n")
	c, err := parseTest([]string{"--token", "flag-token", "--connections", "4", "--json=false"}, env(map[string]string{
		"SPEEDPROBE_SERVER": "http://environment.invalid", "SPEEDPROBE_TOKEN": "env-token", "SPEEDPROBE_CONNECTIONS": "3",
	}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Client.ServerURL != "http://environment.invalid" || c.Client.Token != "flag-token" || c.Client.Connections != 4 || c.JSON {
		t.Fatalf("configuration precedence ignored: %+v", c)
	}
}

func TestDotEnvTokenSourcePrecedence(t *testing.T) {
	dir := dotenvDirectory(t, "SPEEDPROBE_SERVER=http://localhost\nSPEEDPROBE_TOKEN=dotenv-token\n")
	tokenFile := filepath.Join(dir, "tokens")
	if err := os.WriteFile(tokenFile, []byte("token-file-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"flag-file-over-dotenv-token", []string{"--token-file", tokenFile}, nil, "token-file-value"},
		{"env-file-over-dotenv-token", nil, map[string]string{"SPEEDPROBE_TOKEN_FILE": tokenFile}, "token-file-value"},
		{"flag-file-over-env-token", []string{"--token-file", tokenFile}, map[string]string{"SPEEDPROBE_TOKEN": "env-token"}, "token-file-value"},
		{"flag-token-over-env-file", []string{"--token", "flag-token"}, map[string]string{"SPEEDPROBE_TOKEN_FILE": tokenFile}, "flag-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := parseTest(tc.args, env(tc.env), io.Discard)
			if err != nil || c.Client.Token != tc.want {
				t.Fatalf("token source precedence: token=%q err=%v", c.Client.Token, err)
			}
		})
	}
}

func TestDotEnvSyntaxAndLiteralValues(t *testing.T) {
	t.Setenv("SPEEDPROBE_OTHER", "must-not-expand")
	for _, tc := range []struct{ name, contents, want string }{
		{"bom-crlf-export", "\xef\xbb\xbf# client\r\nexport SPEEDPROBE_TOKEN = 'alpha#beta' # comment\r\n", "alpha#beta"},
		{"unquoted-comment", "SPEEDPROBE_TOKEN=alpha#beta # comment\n", "alpha#beta"},
		{"double-quoted-escapes", "SPEEDPROBE_TOKEN=\"alpha\\\\beta\\\"gamma\"\n", "alpha\\beta\"gamma"},
		{"no-expansion", "SPEEDPROBE_TOKEN=\"$SPEEDPROBE_OTHER\"\n", "$SPEEDPROBE_OTHER"},
		{"no-command-substitution", "SPEEDPROBE_TOKEN=\"$(whoami)\"\n", "$(whoami)"},
		{"duplicate-last-wins", "SPEEDPROBE_TOKEN=old\nSPEEDPROBE_TOKEN=new\n", "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dotenvDirectory(t, tc.contents)
			c, err := parseTest(nil, env(map[string]string{"SPEEDPROBE_SERVER": "http://localhost"}), io.Discard)
			if err != nil || c.Client.Token != tc.want {
				t.Fatalf("got token=%q err=%v", c.Client.Token, err)
			}
		})
	}
}

func TestDotEnvErrorsNeverEchoSecretLines(t *testing.T) {
	const secret = "private-secret-not-for-error-output"
	for _, contents := range []string{
		"SPEEDPROBE_TOKEN='" + secret,
		"SPEEDPROBE_TOKEN=\"" + secret + "\" trailing-data",
		secret,
		"9INVALID=" + secret,
		"SPEEDPROBE_TOKEN=" + secret + strings.Repeat("x", 9000),
		"SPEEDPROBE_TOKEN=" + secret + strings.Repeat("x", 1<<20),
		"SPEEDPROBE_TOKEN=" + secret + "\xff",
	} {
		t.Run(fmt.Sprintf("bytes-%d", len(contents)), func(t *testing.T) {
			dotenvDirectory(t, "# client settings\n"+contents)
			_, err := parseTest([]string{"--server", "http://localhost", "--token", "flag-token"}, env(nil), io.Discard)
			if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), ".env") {
				t.Fatalf("expected a safe .env error, got %v", err)
			}
		})
	}
}

func TestDotEnvFileSelectionAndOptOut(t *testing.T) {
	dir := dotenvDirectory(t, "invalid default file")
	custom := filepath.Join(dir, "client.env")
	if err := os.WriteFile(custom, []byte("SPEEDPROBE_SERVER=http://localhost\nSPEEDPROBE_TOKEN=custom-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := parseTest([]string{"--env-file", custom}, env(nil), io.Discard)
	if err != nil || c.Client.Token != "custom-token" {
		t.Fatalf("custom file: %+v %v", c, err)
	}
	c, err = parseTest([]string{"--env-file=", "--server", "http://localhost", "--token", "flag-token"}, env(nil), io.Discard)
	if err != nil || c.Client.Token != "flag-token" {
		t.Fatalf("opt-out: %+v %v", c, err)
	}
	if _, err := parseTest([]string{"--env-file", filepath.Join(dir, "missing.env"), "--server", "http://localhost", "--token", "flag-token"}, env(nil), io.Discard); err == nil {
		t.Fatal("explicitly requested missing file was ignored")
	}
}

func TestOptionalDotEnvAndHelp(t *testing.T) {
	dir := dotenvDirectory(t, "invalid .env with private-secret")
	var help bytes.Buffer
	if _, err := parseTest([]string{"--help"}, env(nil), &help); !errors.Is(err, flag.ErrHelp) || strings.Contains(help.String(), "private-secret") {
		t.Fatalf("help read invalid .env: %v", err)
	}
	if _, err := parseServe([]string{"--tokens", "server-token"}, env(nil), io.Discard); err != nil {
		t.Fatalf("serve unexpectedly read .env: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	if _, err := parseTest([]string{"--server", "http://localhost", "--token", "flag-token"}, env(nil), io.Discard); err != nil {
		t.Fatalf("optional missing .env: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".env"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := parseTest([]string{"--server", "http://localhost", "--token", "flag-token"}, env(nil), io.Discard); err == nil {
		t.Fatal("directory accepted as .env")
	}
}

func TestDotEnvCommandAgainstRealServer(t *testing.T) {
	c := server.DefaultConfig()
	c.Tokens = []string{"dotenv-test-secret"}
	c.BytesPerSecond = 4 << 20
	s, err := server.New(c)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	dotenvDirectory(t, fmt.Sprintf("SPEEDPROBE_SERVER=%s\nSPEEDPROBE_TOKEN=dotenv-test-secret\nSPEEDPROBE_CONNECTIONS=2\nSPEEDPROBE_DURATION=200ms\nSPEEDPROBE_TIMEOUT=1s\nSPEEDPROBE_LATENCY_SAMPLES=2\nSPEEDPROBE_LATENCY_INTERVAL=0\nSPEEDPROBE_JSON=true\n", ts.URL))
	var out, errOut bytes.Buffer
	if err := run(context.Background(), []string{"test"}, &out, &errOut, env(nil)); err != nil {
		t.Fatal(err)
	}
	var result client.Result
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Download.Bytes <= 0 || result.Upload.Bytes <= 0 || errOut.Len() != 0 || strings.Contains(out.String(), "dotenv-test-secret") {
		t.Fatalf("unexpected command output: %s %s", out.String(), errOut.String())
	}
}
