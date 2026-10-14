// Package protocol defines the versioned SpeedProbe wire format.
package protocol

const (
	Version   = "1"
	Prefix    = "/api/v1"
	BlockSize = 64 << 10
)

type Info struct {
	ProtocolVersion    string  `json:"protocol_version"`
	MaxDurationSeconds float64 `json:"max_duration_seconds"`
	MaxBytes           int64   `json:"max_bytes_per_request"`
	MaxConcurrent      int     `json:"max_concurrent"`
	MaxPerToken        int     `json:"max_concurrent_per_token"`
	BytesPerSecond     int64   `json:"bytes_per_second"`
}

type UploadResult struct {
	Bytes          int64   `json:"bytes"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
}

type Error struct {
	Error string `json:"error"`
}
