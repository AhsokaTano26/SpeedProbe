// SpeedProbe is a single-binary, dependency-free network speed tester.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"speedprobe/internal/client"
	"speedprobe/internal/server"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "speedprobe:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, errOut io.Writer, getenv func(string) string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(out, "SpeedProbe — lightweight network speed testing\n\nUsage: speedprobe <serve|test> [options]\n       speedprobe version\n\nRun speedprobe serve --help or speedprobe test --help for options.")
		return err
	}
	switch args[0] {
	case "version", "--version":
		_, err := fmt.Fprintln(out, "SpeedProbe", version)
		return err
	case "serve":
		c, err := parseServe(args[1:], getenv, errOut)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
		return serve(ctx, c, errOut)
	case "test":
		c, err := parseTest(args[1:], getenv, errOut)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
		return runTests(ctx, c, out, errOut, client.Run)
	default:
		return fmt.Errorf("unknown command %q (use serve or test)", args[0])
	}
}

func serve(ctx context.Context, c serveConfig, out io.Writer) error {
	s, err := server.New(c.Server)
	if err != nil {
		return err
	}
	h := s.HTTPServer(c.Addr)
	h.ErrorLog = log.New(out, "http: ", log.LstdFlags)
	if c.TLSCert != "" {
		cert, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
		if err != nil {
			return fmt.Errorf("load TLS certificate: %w", err)
		}
		h.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}}
		// Keep native TLS on HTTP/1.1 as well; a reverse proxy may accept HTTP/2
		// externally, but the official client explicitly uses independent TCP.
		h.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))
	}
	listener, err := net.Listen(c.Network, c.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	scheme := "http"
	if c.TLSCert != "" {
		scheme = "https"
	}
	fmt.Fprintf(out, "SpeedProbe %s listening on %s://%s (%d tokens, max %d transfers, %d per token)\n", version, scheme, listener.Addr(), len(c.Server.Tokens), c.Server.MaxConcurrent, c.Server.MaxPerToken)
	shutdownDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(shutdownDone)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.Shutdown(shutdownCtx); err != nil {
			_ = h.Close()
		}
	})
	defer stop()
	if c.TLSCert != "" {
		err = h.ServeTLS(listener, "", "")
	} else {
		err = h.Serve(listener)
	}
	if errors.Is(err, http.ErrServerClosed) {
		<-shutdownDone
		return nil
	}
	return err
}
