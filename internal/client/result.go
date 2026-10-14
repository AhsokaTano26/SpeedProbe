package client

import (
	"fmt"
	"io"
	"math"
	"sort"
	"time"

	"speedprobe/internal/protocol"
)

type Latency struct {
	SamplesMS []float64 `json:"samples_ms"`
	MeanMS    float64   `json:"mean_ms"`
	MinMS     float64   `json:"min_ms"`
	MaxMS     float64   `json:"max_ms"`
	P50MS     float64   `json:"p50_ms"`
	P95MS     float64   `json:"p95_ms"`
	JitterMS  float64   `json:"jitter_ms"`
}

type Transfer struct {
	Bytes          int64   `json:"bytes"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	Mbps           float64 `json:"mbps"`
}

type Result struct {
	SchemaVersion            int           `json:"schema_version"`
	Server                   string        `json:"server"`
	StartedAt                time.Time     `json:"started_at"`
	Connections              int           `json:"connections"`
	RequestedDurationSeconds float64       `json:"requested_duration_seconds"`
	ServerLimits             protocol.Info `json:"server_limits"`
	Latency                  Latency       `json:"latency"`
	Download                 Transfer      `json:"download"`
	Upload                   Transfer      `json:"upload"`
}

func latencyStats(samples []float64) Latency {
	l := Latency{SamplesMS: append([]float64(nil), samples...)}
	if len(samples) == 0 {
		return l
	}
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	for i, v := range samples {
		l.MeanMS += v
		if i > 0 {
			l.JitterMS += math.Abs(v - samples[i-1])
		}
	}
	l.MeanMS /= float64(len(samples))
	if len(samples) > 1 {
		l.JitterMS /= float64(len(samples) - 1)
	}
	l.MinMS = sorted[0]
	l.MaxMS = sorted[len(sorted)-1]
	l.P50MS = sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		l.P50MS = (sorted[len(sorted)/2-1] + l.P50MS) / 2
	}
	l.P95MS = sorted[int(math.Ceil(float64(len(sorted))*0.95))-1]
	return l
}

func transferResult(bytes int64, elapsed time.Duration) Transfer {
	seconds := elapsed.Seconds()
	return Transfer{Bytes: bytes, ElapsedSeconds: seconds, Mbps: float64(bytes) * 8 / seconds / 1e6}
}

func (r Result) WriteText(w io.Writer) error {
	_, err := fmt.Fprintf(w, "SpeedProbe  •  %s\nConnections: %d   Duration: %.2f s per direction\n\n"+
		"  Download  %10.2f Mbps  (%8.2f MB in %.2f s)\n"+
		"  Upload    %10.2f Mbps  (%8.2f MB in %.2f s)\n"+
		"  Latency   %10.2f ms    (min %.2f / p50 %.2f / p95 %.2f / max %.2f)\n"+
		"  Jitter    %10.2f ms    (%d RTT samples)\n",
		r.Server, r.Connections, r.RequestedDurationSeconds, r.Download.Mbps, float64(r.Download.Bytes)/1e6, r.Download.ElapsedSeconds,
		r.Upload.Mbps, float64(r.Upload.Bytes)/1e6, r.Upload.ElapsedSeconds,
		r.Latency.MeanMS, r.Latency.MinMS, r.Latency.P50MS, r.Latency.P95MS, r.Latency.MaxMS, r.Latency.JitterMS, len(r.Latency.SamplesMS))
	if err == nil && r.ServerLimits.BytesPerSecond > 0 {
		_, err = fmt.Fprintf(w, "\nServer shared bandwidth limit: %.2f Mbps (all tokens, both directions)\n", float64(r.ServerLimits.BytesPerSecond)*8/1e6)
	}
	return err
}
