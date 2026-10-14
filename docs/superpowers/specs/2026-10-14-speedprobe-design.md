# SpeedProbe design

Build a lightweight MIT-licensed Go network speed tester as one binary with `serve` and `test` commands. Use only the Go standard library, no database, and bounded streaming memory. Go 1.23 or newer is required. This implements the user's supplied project brief in the existing clean workspace.

## Protocol and server

Authenticated REST endpoints are `GET /api/v1/info`, `GET /api/v1/ping`, `GET /api/v1/download`, and `POST /api/v1/upload`. Only `/healthz` is public. Bearer tokens are configured through environment variables or a file; SHA-256 digests are compared in constant time. Empty token configuration must fail closed. No tokens appear in logs or results.

Each request has a hard duration bound. Transfers additionally have global and per-token concurrency limits, a byte cap per request (default 1 GiB), and an optional shared payload bytes/second limit. Rate limiting counts both upload and download across every token; it is not a monthly quota. Reject overload with 429. Stream downloads from an immutable 64 KiB random block, consume uploads with a 64 KiB buffer, and acknowledge bytes actually read. Reject compressed uploads and disable compression/cache/buffering in protocol headers. Abort timed-out download responses rather than return a misleading clean EOF.

Listen on a configurable address and `tcp`, `tcp4`, or `tcp6`; the default unspecified address permits the OS dual-stack behavior. Read-header/idle deadlines, bounded headers, graceful signal shutdown, and per-transfer I/O deadlines protect resources. TLS can terminate at Nginx; native certificate/key configuration is also supported.

## Client and measurement

Validate URLs, token, duration, connection count, IP family, and timeouts. Refuse redirects and unexpected compressed/cached responses. Use distinct HTTP/1.1 transports per transfer connection, honor proxy environment variables, and retain TLS certificate verification. Preflight info checks advertised limits. Warm each connection with a ping before each phase. Run latency, download, then upload sequentially.

Latency is HTTP application round-trip time on a reused connection, not ICMP; report mean/min/max/median/p95 and mean absolute successive RTT difference as jitter. Download counts payload reads within one common monotonic window, excluding connection warmup. Upload stops producing payload at the common deadline, waits a bounded period for server acknowledgement, and divides acknowledged bytes by time from phase start through the last acknowledgement; this conservative calculation includes drain and acknowledgement time. Do not claim client-buffered writes reached the server. Any early EOF, request limit, truncation, unauthorized response, or failed worker invalidates the whole test. External cancellation must propagate promptly.

Provide readable terminal output and stable JSON with UTC start time, decimal Mbps, bytes, elapsed seconds, connections and latency samples. Token never appears in output. Duration is per throughput direction. Timing and byte overhead limitations are documented.

## Packaging and verification

Supply multi-stage non-root Docker image, hardened Compose configuration, `.env.example`, Nginx streaming proxy example, Makefile, CI, and a Chinese README with quickstart, every option, API, units, limit semantics, IPv6 and measurement caveats. Preserve the existing MIT license.

Tests cover auth, limits, cancellation, upload acknowledgements, error propagation, request validation, streaming headers, rate accounting and actual client/server interoperability. Run gofmt, `go test -race ./...`, `go vet ./...`, a binary loopback test with JSON, and Linux static compilation. Docker validation is performed if Docker is available.
