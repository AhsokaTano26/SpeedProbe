package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"speedprobe/internal/client"
	"speedprobe/internal/server"
)

type serveConfig struct {
	Addr, Network, TLSCert, TLSKey string
	Server                         server.Config
}

type testConfig struct {
	Client   client.Config
	JSON     bool
	Interval time.Duration
	Count    int
	LogFile  string
}

func setting(getenv func(string) string, name, fallback string) string {
	if value := getenv("SPEEDPROBE_" + name); value != "" {
		return value
	}
	return fallback
}

func flags(command string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { fmt.Fprintf(out, "Usage: speedprobe %s [options]\n\n", command); fs.PrintDefaults() }
	return fs
}

func parseInt(name, value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return n, nil
}

func parseInt64(name, value string) (int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return n, nil
}

func parseDuration(name, value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration such as 10s", name)
	}
	return d, nil
}

func readTokens(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read token file: %w", err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() || stat.Size() > 1<<20 {
		return nil, errors.New("token file must be a regular file no larger than 1 MiB")
	}
	scanner := bufio.NewScanner(io.LimitReader(f, (1<<20)+1))
	scanner.Buffer(make([]byte, 4096), 8192)
	var tokens []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			tokens = append(tokens, line)
		}
		if len(tokens) > 1024 {
			return nil, errors.New("token file contains too many tokens")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.New("invalid token file: line exceeds size limit or read failed")
	}
	return tokens, nil
}

func parseServe(args []string, getenv func(string) string, out io.Writer) (serveConfig, error) {
	c := serveConfig{Server: server.DefaultConfig()}
	fs := flags("serve", out)
	fs.StringVar(&c.Addr, "addr", setting(getenv, "ADDR", ":8080"), "listen address (IPv6: [::]:8080)")
	fs.StringVar(&c.Network, "network", setting(getenv, "NETWORK", "tcp"), "listen network: tcp, tcp4, tcp6")
	fs.StringVar(&c.TLSCert, "tls-cert", setting(getenv, "TLS_CERT", ""), "TLS certificate PEM file")
	fs.StringVar(&c.TLSKey, "tls-key", setting(getenv, "TLS_KEY", ""), "TLS private key PEM file")
	tokens := fs.String("tokens", setting(getenv, "TOKENS", setting(getenv, "TOKEN", "")), "comma-separated bearer tokens (prefer env or token file)")
	tokenFile := fs.String("tokens-file", setting(getenv, "TOKENS_FILE", ""), "file containing one token per line")
	fs.Lookup("tokens").DefValue = ""
	concurrent := fs.String("max-concurrent", setting(getenv, "MAX_CONCURRENT", "32"), "global simultaneous transfer limit")
	perToken := fs.String("max-per-token", setting(getenv, "MAX_PER_TOKEN", "8"), "simultaneous transfers per token")
	maxBytes := fs.String("max-bytes", setting(getenv, "MAX_BYTES", "1073741824"), "payload byte limit per transfer request")
	maxDuration := fs.String("max-duration", setting(getenv, "MAX_DURATION", "60s"), "hard maximum duration per request")
	rate := fs.String("bytes-per-second", setting(getenv, "BYTES_PER_SECOND", "0"), "shared payload bandwidth limit (0 = unlimited)")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() != 0 {
		return c, errors.New("serve does not accept positional arguments")
	}
	if c.Network != "tcp" && c.Network != "tcp4" && c.Network != "tcp6" {
		return c, errors.New("network must be tcp, tcp4 or tcp6")
	}
	_, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return c, errors.New("addr must be host:port (bracket IPv6 addresses)")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 0 || p > 65535 {
		return c, errors.New("invalid listen port")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return c, errors.New("tls-cert and tls-key must be configured together")
	}
	if *tokenFile != "" {
		if *tokens != "" {
			return c, errors.New("configure tokens or tokens-file, not both")
		}
		c.Server.Tokens, err = readTokens(*tokenFile)
		if err != nil {
			return c, err
		}
	} else if *tokens != "" {
		for _, token := range strings.Split(*tokens, ",") {
			c.Server.Tokens = append(c.Server.Tokens, strings.TrimSpace(token))
		}
	}
	if c.Server.MaxConcurrent, err = parseInt("max-concurrent", *concurrent); err != nil {
		return c, err
	}
	if c.Server.MaxPerToken, err = parseInt("max-per-token", *perToken); err != nil {
		return c, err
	}
	if c.Server.MaxBytes, err = parseInt64("max-bytes", *maxBytes); err != nil {
		return c, err
	}
	if c.Server.MaxDuration, err = parseDuration("max-duration", *maxDuration); err != nil {
		return c, err
	}
	if c.Server.BytesPerSecond, err = parseInt64("bytes-per-second", *rate); err != nil {
		return c, err
	}
	_, err = server.New(c.Server)
	return c, err
}

func parseTest(args []string, getenv func(string) string, out io.Writer) (testConfig, error) {
	c := testConfig{Client: client.DefaultConfig()}
	fs := flags("test", out)
	fs.StringVar(&c.Client.ServerURL, "server", setting(getenv, "SERVER", ""), "server URL, optionally with proxy path prefix")
	fs.StringVar(&c.Client.Token, "token", setting(getenv, "TOKEN", ""), "bearer token (prefer env or token file)")
	fs.Lookup("token").DefValue = ""
	tokenFile := fs.String("token-file", setting(getenv, "TOKEN_FILE", ""), "file containing exactly one token")
	fs.StringVar(&c.Client.IPVersion, "ip", setting(getenv, "IP", "auto"), "IP family: auto, 4, 6")
	connections := fs.String("connections", setting(getenv, "CONNECTIONS", "4"), "independent TCP connections per direction")
	duration := fs.String("duration", setting(getenv, "DURATION", "10s"), "measurement duration per direction (minimum 100ms)")
	timeout := fs.String("timeout", setting(getenv, "TIMEOUT", "5s"), "connection/request timeout and upload ACK grace period")
	samples := fs.String("latency-samples", setting(getenv, "LATENCY_SAMPLES", "20"), "number of unloaded HTTP RTT samples")
	interval := fs.String("latency-interval", setting(getenv, "LATENCY_INTERVAL", "50ms"), "pause between RTT samples")
	repeatInterval := fs.String("interval", setting(getenv, "INTERVAL", "0"), "period between test starts (0 = one test; first test starts immediately)")
	repeatCount := fs.String("count", setting(getenv, "COUNT", "0"), "number of scheduled tests (0 = unlimited; requires interval for multiple tests)")
	fs.StringVar(&c.LogFile, "log-file", setting(getenv, "LOG_FILE", ""), "append JSON Lines log (periodic default: speedprobe.jsonl)")
	// String storage with IsBoolFlag retains --json while allowing an explicit
	// command-line value to override a malformed environment value.
	jsonFlag := boolSetting{value: setting(getenv, "JSON", "false")}
	fs.Var(&jsonFlag, "json", "output JSON to stdout (periodic mode: JSON Lines)")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() != 0 {
		return c, errors.New("test does not accept positional arguments")
	}
	var err error
	if *tokenFile != "" {
		if c.Client.Token != "" {
			return c, errors.New("configure token or token-file, not both")
		}
		tokens, err := readTokens(*tokenFile)
		if err != nil {
			return c, err
		}
		if len(tokens) != 1 {
			return c, errors.New("client token file must contain exactly one token")
		}
		c.Client.Token = tokens[0]
	}
	if c.Client.Connections, err = parseInt("connections", *connections); err != nil {
		return c, err
	}
	if c.Client.Duration, err = parseDuration("duration", *duration); err != nil {
		return c, err
	}
	if c.Client.Timeout, err = parseDuration("timeout", *timeout); err != nil {
		return c, err
	}
	if c.Client.LatencySamples, err = parseInt("latency-samples", *samples); err != nil {
		return c, err
	}
	if c.Client.LatencyInterval, err = parseDuration("latency-interval", *interval); err != nil {
		return c, err
	}
	if c.JSON, err = strconv.ParseBool(jsonFlag.value); err != nil {
		return c, errors.New("json must be true or false")
	}
	if c.Interval, err = parseDuration("interval", *repeatInterval); err != nil {
		return c, err
	}
	if c.Count, err = parseInt("count", *repeatCount); err != nil {
		return c, err
	}
	if c.Interval < 0 || c.Count < 0 {
		return c, errors.New("interval and count must be non-negative")
	}
	if c.Interval == 0 && c.Count > 1 {
		return c, errors.New("count greater than 1 requires a positive interval")
	}
	if c.Interval > 0 && c.LogFile == "" {
		c.LogFile = "speedprobe.jsonl"
	}
	return c, c.Client.Validate()
}

type boolSetting struct{ value string }

func (b *boolSetting) String() string         { return b.value }
func (b *boolSetting) Set(value string) error { b.value = value; return nil }
func (*boolSetting) IsBoolFlag() bool         { return true }
