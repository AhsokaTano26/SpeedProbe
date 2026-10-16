package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// applyClientEnv fills only unset options. The flag set already contains
// process-environment defaults and parsed command-line values.
func applyClientEnv(fs *flag.FlagSet, path string, getenv func(string) string) error {
	explicit := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	values, err := readDotEnv(path, !explicit["env-file"])
	if err != nil {
		return err
	}
	// Treat token and token-file as a single choice across configuration layers.
	// Two nonempty sources at the same layer remain an error in parseTest.
	if explicit["token"] && !explicit["token-file"] {
		_ = fs.Set("token-file", "")
	}
	if explicit["token-file"] && !explicit["token"] {
		_ = fs.Set("token", "")
	}
	authSelected := explicit["token"] || explicit["token-file"] || getenv("SPEEDPROBE_TOKEN") != "" || getenv("SPEEDPROBE_TOKEN_FILE") != ""
	fs.VisitAll(func(f *flag.Flag) {
		if err != nil || explicit[f.Name] || f.Name == "env-file" {
			return
		}
		if (f.Name == "token" || f.Name == "token-file") && authSelected {
			return
		}
		key := "SPEEDPROBE_" + strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
		if getenv(key) != "" || values[key] == "" {
			return
		}
		if setErr := fs.Set(f.Name, values[key]); setErr != nil {
			// Do not echo a configuration value, which may contain a token.
			err = fmt.Errorf(".env: invalid value for --%s", f.Name)
		}
	})
	return err
}

func readDotEnv(path string, optional bool) (map[string]string, error) {
	values := make(map[string]string)
	if path == "" {
		return values, nil
	}
	info, err := os.Stat(path)
	if optional && errors.Is(err, os.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read .env file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New(".env must be a regular file no larger than 1 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read .env file: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, errors.New("could not read .env file")
	}
	if len(data) > 1<<20 || !utf8.Valid(data) {
		return nil, errors.New(".env must be UTF-8 and no larger than 1 MiB")
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff")))
	scanner.Buffer(make([]byte, 4096), 8192)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if strings.HasPrefix(text, "export ") || strings.HasPrefix(text, "export\t") {
			text = strings.TrimSpace(text[len("export"):])
		}
		key, raw, ok := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		if !ok || !envName(key) {
			return nil, fmt.Errorf(".env line %d: expected KEY=VALUE", line)
		}
		value, err := envValue(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf(".env line %d: %w", line, err)
		}
		values[key] = value
	}
	if scanner.Err() != nil {
		return nil, fmt.Errorf(".env line %d: line exceeds 8 KiB", line+1)
	}
	return values, nil
}

func envName(key string) bool {
	if key == "" {
		return false
	}
	for i, c := range []byte(key) {
		if c != '_' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func envValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if quote := raw[0]; quote == '\'' || quote == '"' {
		end := 1
		for end < len(raw) {
			if quote == '"' && raw[end] == '\\' {
				end += 2
				continue
			}
			if raw[end] == quote {
				break
			}
			end++
		}
		if end >= len(raw) {
			return "", errors.New("unterminated quoted value")
		}
		if tail := strings.TrimSpace(raw[end+1:]); tail != "" && !strings.HasPrefix(tail, "#") {
			return "", errors.New("unexpected text after quoted value")
		}
		if quote == '\'' {
			return raw[1:end], nil
		}
		value, err := strconv.Unquote(raw[:end+1])
		if err != nil {
			return "", errors.New("invalid escape in double-quoted value")
		}
		return value, nil
	}
	for i := range raw {
		if raw[i] == '#' && (i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t') {
			return strings.TrimSpace(raw[:i]), nil
		}
	}
	return raw, nil
}
