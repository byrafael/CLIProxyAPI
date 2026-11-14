// Package quotahistory records each credential's primary quota window over time,
// so the management panel can chart usage even when no browser had it open.
//
// The model, sampling rules, and stored format mirror the panel's
// src/features/quota/forecast/history.ts (format version 1). The panel merges
// this history with its own samples, so both sides must keep the same keys and units.
package quotahistory

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	// Retention is the history window kept on disk.
	Retention = 30 * 24 * time.Hour
	// heartbeat records an unchanged value at least this often, so coverage stays measurable.
	heartbeat = time.Hour
	// sameReset treats reset instants closer than this as the same window.
	sameReset = 30 * time.Minute

	formatVersion = 1
)

// Point is one sample: when it was taken, the remaining percent, and the reported reset.
type Point struct {
	At        time.Time
	Remaining float64
	ResetsAt  time.Time
}

// Series is one credential window's samples, oldest first.
type Series struct {
	Provider    string
	PeriodHours float64
	Points      []Point
}

// History is keyed by "<credential file name>#<weekly|session>", like the panel.
type History map[string]*Series

// Window is the primary limit read from one usage response.
type Window struct {
	// Name is "weekly" or "session", the panel's PrimaryWindow.
	Name        string
	Remaining   float64
	ResetsAt    time.Time
	PeriodHours float64
}

// SeriesKey matches the panel's seriesKey(accountId, window); accountId is the file name.
func SeriesKey(fileName, window string) string { return fileName + "#" + window }

// Record appends a sample unless it repeats the previous one within the heartbeat,
// then drops samples older than the retention window. It reports whether anything changed.
func (h History) Record(key, provider string, window Window, now time.Time) bool {
	point := Point{At: now, Remaining: window.Remaining, ResetsAt: window.ResetsAt}
	series := h[key]
	changed := false
	if series != nil && len(series.Points) > 0 {
		last := series.Points[len(series.Points)-1]
		gap := now.Sub(last.At)
		unchanged := last.Remaining == point.Remaining && absDuration(last.ResetsAt.Sub(point.ResetsAt)) < sameReset
		if gap >= 0 && !(unchanged && gap < heartbeat) {
			series.Points = append(series.Points, point)
			series.PeriodHours = window.PeriodHours
			series.Provider = provider
			changed = true
		}
	} else {
		h[key] = &Series{Provider: provider, PeriodHours: window.PeriodHours, Points: []Point{point}}
		changed = true
	}
	return h.prune(now) || changed
}

func (h History) prune(now time.Time) bool {
	cutoff := now.Add(-Retention)
	changed := false
	for key, series := range h {
		kept := series.Points[:0:0]
		for _, point := range series.Points {
			if !point.At.Before(cutoff) {
				kept = append(kept, point)
			}
		}
		if len(kept) == len(series.Points) {
			continue
		}
		changed = true
		if len(kept) == 0 {
			delete(h, key)
		} else {
			series.Points = kept
		}
	}
	return changed
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

/* ---------- Stored format: the panel's StoredHistory v1, minute precision ---------- */

type storedSeries struct {
	P string       `json:"p"`
	H float64      `json:"h"`
	D [][3]float64 `json:"d"`
}

type stored struct {
	Version int                     `json:"version"`
	Series  map[string]storedSeries `json:"series"`
}

// MarshalJSON writes the panel's compact v1 format.
func (h History) MarshalJSON() ([]byte, error) {
	out := stored{Version: formatVersion, Series: make(map[string]storedSeries, len(h))}
	for key, series := range h {
		rows := make([][3]float64, 0, len(series.Points))
		for _, point := range series.Points {
			rows = append(rows, [3]float64{
				math.Round(float64(point.At.UnixMilli()) / float64(time.Minute.Milliseconds())),
				math.Round(point.Remaining*10) / 10,
				math.Round(float64(point.ResetsAt.UnixMilli()) / float64(time.Minute.Milliseconds())),
			})
		}
		out.Series[key] = storedSeries{P: series.Provider, H: series.PeriodHours, D: rows}
	}
	return json.Marshal(out)
}

// Parse reads the v1 format. Malformed series are dropped rather than trusted.
func Parse(raw []byte) History {
	history := History{}
	var in stored
	if len(raw) == 0 || json.Unmarshal(raw, &in) != nil || in.Version != formatVersion {
		return history
	}
	minute := time.Minute.Milliseconds()
	for key, entry := range in.Series {
		if entry.P == "" || entry.H <= 0 || len(entry.D) == 0 {
			continue
		}
		points := make([]Point, 0, len(entry.D))
		for _, row := range entry.D {
			points = append(points, Point{
				At:        time.UnixMilli(int64(row[0]) * minute),
				Remaining: math.Min(100, math.Max(0, row[1])),
				ResetsAt:  time.UnixMilli(int64(row[2]) * minute),
			})
		}
		sort.Slice(points, func(i, j int) bool { return points[i].At.Before(points[j].At) })
		history[key] = &Series{Provider: entry.P, PeriodHours: entry.H, Points: points}
	}
	return history
}

// Load reads the history file; a missing or unreadable file is an empty history.
func Load(path string) History {
	raw, err := os.ReadFile(path)
	if err != nil {
		return History{}
	}
	return Parse(raw)
}

// Save writes the history atomically, so a crash never leaves a truncated file.
func Save(path string, history History) error {
	raw, err := json.Marshal(history)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".quota-history-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err = tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
