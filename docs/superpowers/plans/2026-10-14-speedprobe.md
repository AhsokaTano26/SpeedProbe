# SpeedProbe Implementation Plan

> **For agentic workers:** Implement inline using the executing-plans and test-driven-development workflows.

**Goal:** Deliver the user's complete single-binary network speed testing project.

**Architecture:** Separate protocol types, server streaming and admission control, client measurement, and CLI configuration. Bound memory and I/O lifetime at each layer; count upload at the receiver.

**Tech Stack:** Go standard library; Docker/Compose and Nginx examples.

**Spec:** `docs/superpowers/specs/2026-10-14-speedprobe-design.md`

## Global Constraints

- Go 1.23 or newer; no external Go dependencies or database.
- Default per-request byte cap is 1 GiB; fixed 64 KiB streaming buffers.
- Authenticated API except `/healthz`; no token logging; TLS verification enabled.
- Common monotonic transfer windows and server-acknowledged upload bytes.

## Review Focus

- Slow or disconnected peers must release transfer slots and I/O deadlines.
- Invalid or duplicate configuration must fail rather than silently relax limits.
- A compressed, cached, redirected, or early-terminated response must not produce a success result.
- A buffered upload acknowledgement must not inflate upload throughput.
- IPv6 URLs, path-prefixed reverse proxies and externally canceled clients must work.

### Task 1: Streaming server and protocol

Files: `internal/protocol/protocol.go`, `internal/server/{server,limits}.go`, `internal/server/*_test.go`.

Produces: `server.DefaultConfig() Config`, `server.New(Config) (*Server, error)`, `(*Server).Handler() http.Handler`, `(*Server).HTTPServer(string) *http.Server`, and shared protocol info/upload structures.

- [x] Write and run failing tests for missing auth, multiple tokens, download byte caps and streaming headers, upload acknowledgements and rejection, duration bounds, overload/release, and shared rate limits.
- [x] Implement immutable random streaming data, hashed-token admission, deadline-safe HTTP handlers and context-aware shared rate limiter.
- [x] Verify `go test ./internal/server`.

### Task 2: Client measurement

Files: `internal/client/{client,transfer,result}.go`, `internal/client/*_test.go`.

Consumes server API. Produces `client.Run(context.Context, Config) (Result, error)` and JSON/terminal result types.

- [x] Write and run failing tests for RTT statistics, URL/config validation, real concurrent interoperability, upload byte accounting, cancellation and refusal of redirects/compression/early EOF.
- [x] Implement separate warmed HTTP/1.1 connections, common timed windows, acknowledged uploads and bounded response decoding.
- [x] Verify `go test ./internal/client ./internal/server`.

### Task 3: CLI and deployable project

Files: `cmd/speedprobe/{main,config}.go`, `cmd/speedprobe/*_test.go`, `Dockerfile`, `compose.yaml`, `.env.example`, `deploy/nginx.conf`, `Makefile`, `.github/workflows/ci.yml`, `README.md`.

- [x] Write and run failing CLI/config tests for environment precedence, invalid values, token files, JSON output and help.
- [x] Implement flags, environment parsing, signal handling, native TLS and output.
- [x] Add documentation and packaging, preserving MIT license.
- [x] Verify formatting, race tests, vet, Linux static build and real binary serve/test smoke tests. Review the final source for resource cleanup and honest measurement semantics.

## Verification and review record

- Complete `go test -race -count=1 ./...`, `go vet ./...`, formatting and Linux static compilation passed.
- Native binary smoke tests passed for IPv4 and IPv6, with four connections, both output modes, multiple tokens, unauthorized failure and signal shutdown.
- Docker image built; Compose configuration validated; actual upload/download succeeded through the non-root, read-only scratch container.
- Independent read-only review found one important issue: net/http could drain rejected request bodies after sending an error, retaining sockets outside transfer admission. `TestRejectedUploadClosesWithoutDraining` reproduced both unauthorized and oversize cases before the fix; expiring the read deadline on rejection passed the regression and complete race suite.
- Implementation refinement: upload readers pace against the advertised server bandwidth ceiling to avoid excessive buffered tails on short limited tests; shared bucket grants are queued to prevent competing connections from starving each other. Upload throughput remains based on acknowledged bytes and the complete acknowledgement window.
- Work was implemented in the user's existing workspace. No repository commits, remote pushes or public deployment were performed.
