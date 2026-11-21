// Package incidents records anonymized, local resource-pressure transitions.
package incidents

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type metric struct {
	ID      string  `json:"metric_id"`
	Value   float64 `json:"value"`
	Unit    string  `json:"unit"`
	Quality string  `json:"quality"`
}

type evidence struct {
	Window   int      `json:"window_seconds"`
	Coverage float64  `json:"coverage_ratio"`
	Metrics  []metric `json:"metrics"`
}

type Event struct {
	Version    int               `json:"schema_version"`
	Device     string            `json:"device_alias"`
	EventID    string            `json:"event_id"`
	IncidentID string            `json:"incident_id"`
	Kind       string            `json:"kind"`
	Severity   string            `json:"severity"`
	Rule       string            `json:"rule_id"`
	ObservedAt time.Time         `json:"observed_at"`
	Scope      map[string]string `json:"scope"`
	Evidence   evidence          `json:"evidence"`
}

type State struct {
	Device      string
	Destination string
	Active      map[string]string
	Outbox      []Event
	NextAttempt time.Time `json:",omitzero"`
	Failures    int       `json:",omitempty"`
}

type Recorder struct {
	dir         string
	destination string
	target      string
	client      *http.Client
	state       State
	counts      map[string]int
}

const eventLifetime = 24 * time.Hour

func Open(dir, endpoint string) (*Recorder, error) {
	target := strings.TrimSpace(endpoint)
	if target != "" {
		u, err := url.Parse(target)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.ForceQuery || u.RawQuery != "" || strings.Contains(target, "#") {
			return nil, fmt.Errorf("webhook requires an HTTPS URL without query, fragment or credentials")
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(target))
	r := &Recorder{dir: dir, target: target, destination: hex.EncodeToString(hash[:]), counts: make(map[string]int),
		client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	body, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err == nil {
		if err = json.Unmarshal(body, &r.state); err != nil {
			return nil, fmt.Errorf("invalid incident state")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if r.state.Device == "" {
		r.state.Device = id()
	}
	if r.state.Destination != r.destination {
		r.state.Outbox = nil
		r.state.Destination = r.destination
		r.state.NextAttempt = time.Time{}
		r.state.Failures = 0
	}
	r.state.Failures = max(0, min(r.state.Failures, 8))
	if r.state.Active == nil {
		r.state.Active = make(map[string]string)
	}
	return r, r.save()
}

func id() string {
	var b [12]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (r *Recorder) save() error {
	data, err := json.Marshal(r.state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(r.dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(r.dir, "state.json"))
}

func (r *Recorder) Observe(now time.Time, cpu, memory *float64) error {
	for _, entry := range []struct {
		rule, id, unit          string
		value                   *float64
		open, recover           float64
		openCount, recoverCount int
	}{
		{"cpu.sustained_high", "cpu.used_ratio", "ratio", cpu, .9, .8, 18, 9},
		{"memory.low_available", "memory.available_bytes", "bytes", memory, 512 << 20, 768 << 20, 12, 9},
	} {
		if entry.value == nil {
			delete(r.counts, entry.rule)
			continue
		}
		incident := r.state.Active[entry.rule]
		opening := incident == ""
		match := (opening && entry.rule == "cpu.sustained_high" && *entry.value >= entry.open) ||
			(opening && entry.rule == "memory.low_available" && *entry.value < entry.open) ||
			(!opening && entry.rule == "cpu.sustained_high" && *entry.value < entry.recover) ||
			(!opening && entry.rule == "memory.low_available" && *entry.value >= entry.recover)
		if !match {
			delete(r.counts, entry.rule)
			continue
		}
		r.counts[entry.rule]++
		limit := entry.recoverCount
		if opening {
			limit = entry.openCount
		}
		if r.counts[entry.rule] < limit {
			continue
		}
		kind, severity := "recovered", "info"
		if opening {
			kind, severity, incident = "opened", "warning", id()
		}
		e := Event{1, r.state.Device, id(), incident, kind, severity, entry.rule, now.UTC(),
			map[string]string{"kind": "windows_session", "scope_id": "local"},
			evidence{limit * 5, 1, []metric{{entry.id, *entry.value, entry.unit, "ok"}}}}
		if err := r.append(e); err != nil {
			return err
		}
		if opening {
			r.state.Active[entry.rule] = incident
		} else {
			delete(r.state.Active, entry.rule)
		}
		if r.target != "" {
			r.state.Outbox = append(r.state.Outbox, e)
			if len(r.state.Outbox) > 128 {
				r.state.Outbox = r.state.Outbox[len(r.state.Outbox)-128:]
			}
		}
		delete(r.counts, entry.rule)
		if err := r.save(); err != nil {
			return err
		}
	}
	return nil
}

func (r *Recorder) append(event Event) error {
	path := filepath.Join(r.dir, "events-v1.ndjson")
	if info, err := os.Stat(path); err == nil && info.Size() > 1<<20 {
		if err := os.Rename(path, path+".old"); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(file).Encode(event)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (r *Recorder) Deliver(ctx context.Context, now time.Time) error {
	if r.target == "" || len(r.state.Outbox) == 0 {
		return nil
	}
	// Expire queued events even during a receiver-wide retry delay.
	expired := 0
	for _, event := range r.state.Outbox {
		if now.Sub(event.ObservedAt) <= eventLifetime {
			break
		}
		expired++
	}
	if expired != 0 {
		previous := r.state.Outbox
		r.state.Outbox = previous[expired:]
		if err := r.save(); err != nil {
			r.state.Outbox = previous
			return err
		}
	}
	if len(r.state.Outbox) == 0 || r.state.NextAttempt.After(now) {
		return nil
	}
	event := r.state.Outbox[0]
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.target, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid event destination")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", event.EventID)
	resp, err := r.client.Do(req)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			previous := r.state.Outbox
			r.state.Outbox = previous[1:]
			r.state.NextAttempt = now.Add(10 * time.Second)
			r.state.Failures = 0
			if err := r.save(); err != nil {
				r.state.Outbox = previous
				return err
			}
			return nil
		}
	}

	r.state.Failures = min(r.state.Failures+1, 8)
	delay := min(time.Duration(1<<r.state.Failures)*5*time.Second, 15*time.Minute)
	deliveryErr := errors.New("event endpoint unavailable")
	if err == nil {
		deliveryErr = fmt.Errorf("event endpoint returned HTTP %d", resp.StatusCode)
		switch {
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable:
			delay = retryAfter(resp.Header.Get("Retry-After"), now, delay)
		case resp.StatusCode < 500:
			delay = time.Hour
		}
	}
	r.state.NextAttempt = now.Add(delay)
	if err := r.save(); err != nil {
		return errors.Join(deliveryErr, errors.New("could not save incident retry state"))
	}
	return deliveryErr
}

func retryAfter(value string, now time.Time, fallback time.Duration) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		return time.Duration(min(seconds, int64(eventLifetime/time.Second))) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		return min(max(when.Sub(now), 0), eventLifetime)
	}
	return fallback
}
