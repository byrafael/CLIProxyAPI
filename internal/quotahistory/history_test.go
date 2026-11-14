package quotahistory

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// panelFixture is the output of the panel's serializeHistory (forecast/history.ts)
// for two series. The panel and the proxy must read each other's format.
const panelFixture = `{"version":1,"series":{"codex-a.json#weekly":{"p":"codex","h":168,"d":[[29000000,81.3,29005000],[29000060,80,29005000]]},"claude-b.json#session":{"p":"claude","h":5,"d":[[29000010,42.5,29000200]]}}}`

func minutes(n int64) time.Time { return time.UnixMilli(n * time.Minute.Milliseconds()) }

func TestParseReadsThePanelFormat(t *testing.T) {
	history := Parse([]byte(panelFixture))
	codex := history["codex-a.json#weekly"]
	if codex == nil || codex.Provider != "codex" || codex.PeriodHours != 168 || len(codex.Points) != 2 {
		t.Fatalf("codex series = %+v", codex)
	}
	if !codex.Points[0].At.Equal(minutes(29000000)) || codex.Points[0].Remaining != 81.3 || !codex.Points[0].ResetsAt.Equal(minutes(29005000)) {
		t.Fatalf("first codex point = %+v", codex.Points[0])
	}
	if claude := history["claude-b.json#session"]; claude == nil || claude.Points[0].Remaining != 42.5 {
		t.Fatalf("claude series = %+v", claude)
	}
}

func TestMarshalRoundTripsThePanelFormat(t *testing.T) {
	raw, err := json.Marshal(Parse([]byte(panelFixture)))
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(panelFixture), &want); err != nil {
		t.Fatal(err)
	}
	gotRaw, _ := json.Marshal(got)
	wantRaw, _ := json.Marshal(want)
	if string(gotRaw) != string(wantRaw) {
		t.Fatalf("round trip\n got %s\nwant %s", gotRaw, wantRaw)
	}
}

func TestParseDropsMalformedInput(t *testing.T) {
	for _, raw := range []string{"", "not json", `{"version":2,"series":{}}`, `{"version":1,"series":{"k":{"p":"","h":5,"d":[[1,2,3]]}}}`} {
		if got := Parse([]byte(raw)); len(got) != 0 {
			t.Fatalf("Parse(%q) = %v, want empty", raw, got)
		}
	}
	clamped := Parse([]byte(`{"version":1,"series":{"k":{"p":"codex","h":5,"d":[[10,140,20],[5,-3,20]]}}}`))["k"]
	if clamped.Points[0].Remaining != 0 || clamped.Points[1].Remaining != 100 || !clamped.Points[0].At.Before(clamped.Points[1].At) {
		t.Fatalf("clamp and sort = %+v", clamped.Points)
	}
}

func TestRecordSkipsRepeatsUntilTheHeartbeat(t *testing.T) {
	history := History{}
	start := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	window := Window{Name: "weekly", Remaining: 80, ResetsAt: start.Add(48 * time.Hour), PeriodHours: 168}
	key := SeriesKey("codex-a.json", "weekly")

	if !history.Record(key, "codex", window, start) {
		t.Fatal("first sample not recorded")
	}
	if history.Record(key, "codex", window, start.Add(15*time.Minute)) {
		t.Fatal("unchanged sample inside the heartbeat was recorded")
	}
	if !history.Record(key, "codex", window, start.Add(61*time.Minute)) {
		t.Fatal("heartbeat sample not recorded")
	}
	window.Remaining = 75
	if !history.Record(key, "codex", window, start.Add(70*time.Minute)) {
		t.Fatal("changed sample not recorded")
	}
	if history.Record(key, "codex", window, start.Add(-time.Minute)) {
		t.Fatal("sample older than the last one was recorded")
	}
	if got := len(history[key].Points); got != 3 {
		t.Fatalf("points = %d, want 3", got)
	}
}

func TestRecordDropsSamplesPastRetention(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	history := History{
		"old#weekly": {Provider: "codex", PeriodHours: 168, Points: []Point{{At: start, Remaining: 50, ResetsAt: start}}},
	}
	later := start.Add(Retention + time.Hour)
	history.Record("new#weekly", "codex", Window{Remaining: 90, ResetsAt: later, PeriodHours: 168}, later)
	if _, ok := history["old#weekly"]; ok {
		t.Fatal("series older than retention kept")
	}
	if _, ok := history["new#weekly"]; !ok {
		t.Fatal("new series missing")
	}
}

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-history.json")
	if got := Load(path); len(got) != 0 {
		t.Fatalf("missing file loaded %v", got)
	}
	want := Parse([]byte(panelFixture))
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if len(got) != 2 || len(got["codex-a.json#weekly"].Points) != 2 {
		t.Fatalf("loaded %+v", got)
	}
}
