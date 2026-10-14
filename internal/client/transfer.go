package client

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"speedprobe/internal/protocol"
)

// timedReader supplies an unbounded, incompressible upload without a pipe or
// producer goroutine. EOF ends the chunked request normally, allowing an ACK.
type timedReader struct {
	ctx    context.Context
	end    time.Time
	block  []byte
	offset int
	bytes  atomic.Int64
	rate   float64
	next   time.Time
}

func (r *timedReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if !time.Now().Before(r.end) {
		return 0, io.EOF
	}
	n := min(len(p), len(r.block)-r.offset)
	if r.rate > 0 && n > 0 {
		n = min(n, max(1, int(r.rate/20)))
		r.next = r.next.Add(time.Duration(float64(n) / r.rate * float64(time.Second)))
		until := r.next
		if until.After(r.end) {
			until = r.end
		}
		if delay := time.Until(until); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-r.ctx.Done():
				timer.Stop()
				return 0, r.ctx.Err()
			case <-timer.C:
			}
		}
		if !time.Now().Before(r.end) {
			return 0, io.EOF
		}
	}
	n = copy(p[:n], r.block[r.offset:])
	r.offset = (r.offset + n) % len(r.block)
	r.bytes.Add(int64(n))
	return n, nil
}

type workerResult struct {
	bytes    int64
	finished time.Time
	err      error
}

func (r *runner) measure(parent context.Context, upload bool) (Transfer, error) {
	clients := make([]*http.Client, r.config.Connections)
	defer func() {
		for _, h := range clients {
			if h != nil {
				h.CloseIdleConnections()
			}
		}
	}()
	// Every worker owns a transport, so HTTP/2 multiplexing and connection
	// pooling cannot turn N requested connections into fewer TCP connections.
	warmCtx, warmCancel := context.WithCancel(parent)
	var warm sync.WaitGroup
	errorsCh := make(chan error, len(clients))
	for i := range clients {
		clients[i] = r.httpClient()
		warm.Add(1)
		go func(h *http.Client) {
			defer warm.Done()
			if _, err := r.ping(warmCtx, h); err != nil {
				errorsCh <- err
				warmCancel()
			}
		}(clients[i])
	}
	warm.Wait()
	warmCancel()
	close(errorsCh)
	for err := range errorsCh {
		return Transfer{}, fmt.Errorf("connection warmup: %w", err)
	}
	if err := parent.Err(); err != nil {
		return Transfer{}, err
	}
	var block []byte
	if upload {
		block = make([]byte, protocol.BlockSize)
		if _, err := rand.Read(block); err != nil {
			return Transfer{}, err
		}
	}
	start := time.Now()
	end := start.Add(r.config.Duration)
	stop := end
	if upload {
		stop = stop.Add(r.config.Timeout)
	}
	ctx, cancel := context.WithDeadline(parent, stop)
	defer cancel()
	results := make(chan workerResult, len(clients))
	serverDuration := min(r.config.Duration+r.config.Timeout, time.Duration(r.info.MaxDurationSeconds*float64(time.Second)))
	for _, h := range clients {
		go func(h *http.Client) {
			var wr workerResult
			if upload {
				wr = r.upload(ctx, h, end, serverDuration, block)
			} else {
				wr = r.download(ctx, h, end, serverDuration)
			}
			results <- wr
		}(h)
	}
	var total int64
	last := end
	var firstErr error
	for range clients {
		wr := <-results
		if wr.err != nil && firstErr == nil {
			firstErr = wr.err
			cancel()
		}
		total += wr.bytes
		if wr.finished.After(last) {
			last = wr.finished
		}
	}
	if err := parent.Err(); err != nil {
		return Transfer{}, err
	}
	if firstErr != nil {
		return Transfer{}, firstErr
	}
	if total == 0 {
		return Transfer{}, errors.New("no payload transferred")
	}
	elapsed := r.config.Duration
	if upload {
		elapsed = last.Sub(start)
	}
	return transferResult(total, elapsed), nil
}

func (r *runner) download(ctx context.Context, h *http.Client, end time.Time, serverDuration time.Duration) workerResult {
	resp, err := r.request(ctx, h, "GET", "/download?duration="+serverDuration.String(), nil)
	if err != nil {
		return workerResult{err: err}
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/octet-stream" {
		return workerResult{err: errors.New("unexpected download content type")}
	}
	buffer := make([]byte, protocol.BlockSize)
	var bytes int64
	for {
		n, err := resp.Body.Read(buffer)
		now := time.Now()
		if now.Before(end) {
			bytes += int64(n)
		}
		if err != nil {
			if !now.Before(end) && ctx.Err() != nil && bytes > 0 {
				return workerResult{bytes: bytes, finished: now}
			}
			if errors.Is(err, io.EOF) {
				err = errors.New("early download EOF: byte limit or proxy buffering; increase server max-bytes or reduce duration")
			}
			return workerResult{err: err}
		}
		if !now.Before(end) {
			return workerResult{bytes: bytes, finished: now}
		}
	}
}

func (r *runner) upload(ctx context.Context, h *http.Client, end time.Time, serverDuration time.Duration, block []byte) workerResult {
	body := &timedReader{ctx: ctx, end: end, block: block}
	if r.info.BytesPerSecond > 0 {
		body.rate = float64(r.info.BytesPerSecond) / float64(r.config.Connections)
		body.next = end.Add(-r.config.Duration)
	}
	resp, err := r.request(ctx, h, "POST", "/upload?duration="+serverDuration.String(), body)
	if err != nil {
		return workerResult{err: err}
	}
	defer resp.Body.Close()
	var ack protocol.UploadResult
	if err := decodeResponse(resp, &ack); err != nil {
		return workerResult{err: fmt.Errorf("upload acknowledgement: %w", err)}
	}
	if time.Now().Before(end) || ack.Bytes <= 0 || ack.Bytes != body.bytes.Load() || ack.ElapsedSeconds <= 0 {
		return workerResult{err: errors.New("invalid or incomplete upload acknowledgement")}
	}
	return workerResult{bytes: ack.Bytes, finished: time.Now()}
}
