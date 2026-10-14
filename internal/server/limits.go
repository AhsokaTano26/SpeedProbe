package server

import (
	"context"
	"sync"
	"time"

	"speedprobe/internal/protocol"
)

// admission bounds memory by bounding the number of active streaming handlers.
// Token identities are indexes, so arbitrary input cannot grow the map.
type admission struct {
	mu                      sync.Mutex
	active                  int
	perToken                []int
	globalLimit, tokenLimit int
}

func (a *admission) acquire(token int) (func(), bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active >= a.globalLimit || a.perToken[token] >= a.tokenLimit {
		return nil, false
	}
	a.active++
	a.perToken[token]++
	return func() {
		a.mu.Lock()
		a.active--
		a.perToken[token]--
		a.mu.Unlock()
	}, true
}

// rateLimiter is a shared token bucket with at most 50 ms of burst capacity.
// Waiting does not reserve future capacity: cancellation cannot leave debt.
type rateLimiter struct {
	turn    chan struct{}
	rate    int64
	quantum int
	tokens  float64
	last    time.Time
}

func newRateLimiter(rate int64) *rateLimiter {
	quantum := protocol.BlockSize
	if rate > 0 && rate/20 < int64(quantum) {
		quantum = int(max(int64(1), rate/20))
	}
	l := &rateLimiter{rate: rate, quantum: quantum, last: time.Now(), turn: make(chan struct{}, 1)}
	l.turn <- struct{}{}
	return l
}

func (l *rateLimiter) wait(ctx context.Context, n int) error {
	if l.rate == 0 || n == 0 {
		return ctx.Err()
	}
	// Queue chunk grants through a cancellable channel. Competing timers must
	// not let one fast stream repeatedly consume another stream's allowance.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.turn:
	}
	defer func() { l.turn <- struct{}{} }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := time.Now()
		l.tokens = min(float64(l.quantum), l.tokens+now.Sub(l.last).Seconds()*float64(l.rate))
		l.last = now
		if l.tokens >= float64(n) {
			l.tokens -= float64(n)
			return nil
		}
		delay := time.Duration((float64(n) - l.tokens) / float64(l.rate) * float64(time.Second))
		timer := time.NewTimer(max(delay, time.Microsecond))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
