// Package agentwire defines the resident monitoring protocol. Acknowledgements
// cover committed samples, not receipt of a WebSocket frame.
package agentwire

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"net/url"
	"strings"
	"time"
)

const (
	Protocol      = 2
	MaxBatch      = 32
	MaxFrame      = 1 << 20
	MaxAge        = 24 * time.Hour
	MaxQueueBytes = 64 << 20
)

var ErrSampleTime = errors.New("sample capture time is outside the accepted window")

type Config struct {
	HubURL          string `json:"hub_url"`
	Token           string `json:"token"`
	StateDir        string `json:"state_dir"`
	IntervalSeconds int    `json:"interval_seconds"`
	AllowHTTP       bool   `json:"allow_http,omitempty"`
}

func (c Config) Validate() error {
	u, err := url.Parse(c.HubURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && (!c.AllowHTTP || u.Scheme != "http")) {
		return errors.New("agent Hub URL must be HTTPS (HTTP requires explicit opt-in), without credentials, query or fragment")
	}
	if len(c.Token) != 64 || c.StateDir == "" || c.IntervalSeconds < 1 || c.IntervalSeconds > 60 {
		return errors.New("invalid agent configuration")
	}
	if _, err := hex.DecodeString(c.Token); err != nil {
		return errors.New("invalid agent token")
	}
	return nil
}

func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

type Sample struct {
	ID          string            `json:"id"`
	BootID      string            `json:"boot_id"`
	Time        time.Time         `json:"time"`
	CPU         *float64          `json:"cpu,omitempty"`
	MemoryUsed  *int64            `json:"memory_used,omitempty"`
	MemoryTotal *int64            `json:"memory_total,omitempty"`
	SwapUsed    *int64            `json:"swap_used,omitempty"`
	SwapTotal   *int64            `json:"swap_total,omitempty"`
	DiskUsed    *int64            `json:"disk_used,omitempty"`
	DiskTotal   *int64            `json:"disk_total,omitempty"`
	Load1       *float64          `json:"load_1,omitempty"`
	Load5       *float64          `json:"load_5,omitempty"`
	Load15      *float64          `json:"load_15,omitempty"`
	NetRecv     *int64            `json:"net_recv,omitempty"`
	NetSent     *int64            `json:"net_sent,omitempty"`
	DiskRead    *int64            `json:"disk_read,omitempty"`
	DiskWrite   *int64            `json:"disk_write,omitempty"`
	Uptime      *int64            `json:"uptime,omitempty"`
	OS          *string           `json:"os,omitempty"`
	CPUModel    *string           `json:"cpu_model,omitempty"`
	Kernel      *string           `json:"kernel,omitempty"`
	Errors      map[string]string `json:"errors,omitempty"`
}

func (s Sample) Validate(now time.Time) error {
	if len(s.ID) != 32 {
		return errors.New("invalid sample id")
	}
	if _, err := hex.DecodeString(s.ID); err != nil {
		return errors.New("invalid sample id")
	}
	if len(s.BootID) == 0 || len(s.BootID) > 128 {
		return errors.New("invalid boot id")
	}
	for _, v := range []*float64{s.CPU, s.Load1, s.Load5, s.Load15} {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0) {
			return errors.New("invalid metric")
		}
	}
	if s.CPU != nil && *s.CPU > 100 {
		return errors.New("invalid cpu percent")
	}
	for _, v := range []*int64{s.MemoryUsed, s.MemoryTotal, s.SwapUsed, s.SwapTotal, s.DiskUsed, s.DiskTotal, s.NetRecv, s.NetSent, s.DiskRead, s.DiskWrite, s.Uptime} {
		if v != nil && *v < 0 {
			return errors.New("invalid counter")
		}
	}
	for _, pair := range [][2]*int64{{s.MemoryUsed, s.MemoryTotal}, {s.SwapUsed, s.SwapTotal}, {s.DiskUsed, s.DiskTotal}} {
		if pair[0] != nil && pair[1] != nil && *pair[0] > *pair[1] {
			return errors.New("used exceeds total")
		}
	}
	for _, v := range []*string{s.OS, s.CPUModel, s.Kernel} {
		if v != nil && len(*v) > 512 {
			return errors.New("metadata too long")
		}
	}
	if err := ValidateErrors(s.Errors); err != nil {
		return err
	}
	if s.Time.Before(now.Add(-MaxAge-time.Hour)) || s.Time.After(now.Add(5*time.Minute)) {
		return ErrSampleTime
	}
	return nil
}

func ValidateErrors(m map[string]string) error {
	if len(m) > 16 {
		return errors.New("too many collector errors")
	}
	for k, v := range m {
		if len(k) > 32 || len(v) > 256 || strings.ContainsAny(k, "\r\n") {
			return errors.New("invalid collector error")
		}
	}
	return nil
}

type Request struct {
	Protocol int      `json:"protocol"`
	Version  string   `json:"version"`
	Kind     string   `json:"kind"`
	Latest   *Sample  `json:"latest,omitempty"`
	Samples  []Sample `json:"samples,omitempty"`
	Pending  int      `json:"pending"`
	Dropped  uint64   `json:"dropped"`
}

func (r Request) Validate(now time.Time) error {
	if r.Protocol != Protocol || r.Version == "" || len(r.Version) > 128 || (r.Kind != "heartbeat" && r.Kind != "samples") || r.Pending < 0 || r.Pending > 100000 || len(r.Samples) > MaxBatch {
		return errors.New("invalid agent message")
	}
	if r.Kind == "heartbeat" && len(r.Samples) > 0 {
		return errors.New("heartbeat contains samples")
	}
	if r.Kind == "samples" && len(r.Samples) == 0 {
		return errors.New("sample batch is empty")
	}
	if r.Latest != nil {
		if err := r.Latest.Validate(now); err != nil && !errors.Is(err, ErrSampleTime) {
			return err
		}
	}
	for _, s := range r.Samples {
		if err := s.Validate(now); err != nil && !errors.Is(err, ErrSampleTime) {
			return err
		}
	}
	return nil
}

type Response struct {
	Ack        []string  `json:"ack,omitempty"`
	Rejected   []string  `json:"rejected,omitempty"`
	Retry      bool      `json:"retry,omitempty"`
	ServerTime time.Time `json:"server_time"`
}
