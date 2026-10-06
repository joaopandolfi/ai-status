package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLiveFiltersDeadPIDs(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UnixMilli()
	writeFile(t, filepath.Join(root, "sessions", "live.json"), `{"pid":`+strconv.Itoa(os.Getpid())+`,"sessionId":"s1","cwd":"/tmp/x","name":"x","startedAt":`+strconv.FormatInt(now, 10)+`,"qwenVersion":"0.25.0","kind":"tui"}`)
	writeFile(t, filepath.Join(root, "sessions", "dead.json"), `{"pid":999999999,"sessionId":"s2","cwd":"/tmp/y","name":"y","startedAt":`+strconv.FormatInt(now-1000, 10)+`,"qwenVersion":"0.25.0","kind":"tui"}`)
	writeFile(t, filepath.Join(root, "sessions", "garbage.json"), `{broken`)

	got, err := Store{Root: root}.Live()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "s1" {
		t.Fatalf("esperava só s1 viva, got %+v", got)
	}
}

func TestUsageParsesAndSkipsBadLines(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	cur := "token-usage-" + now.Format("2006-01") + ".jsonl"
	prev := now.AddDate(0, -1, 0)
	prevFile := "token-usage-" + prev.Format("2006-01") + ".jsonl"
	writeFile(t, filepath.Join(root, "usage", cur),
		`{"timestamp":"`+now.Format(time.RFC3339)+`","sessionId":"a","model":"m1","source":"main","inputTokens":100,"outputTokens":10,"cachedTokens":5,"thoughtsTokens":2,"totalTokens":112,"apiDurationMs":500}`+"\n"+
			`linha-invalida`+"\n"+
			`{"timestamp":"`+now.Add(-time.Hour).Format(time.RFC3339)+`","sessionId":"a","model":"m2","source":"Explore","inputTokens":40,"outputTokens":4,"totalTokens":44,"apiDurationMs":100}`+"\n",
	)
	writeFile(t, filepath.Join(root, "usage", prevFile),
		`{"timestamp":"`+prev.Format(time.RFC3339)+`","sessionId":"b","model":"m1","source":"main","inputTokens":1,"outputTokens":1,"totalTokens":2,"apiDurationMs":10}`+"\n",
	)

	rows, err := Store{Root: root}.Usage(prev.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("esperava 3 linhas, got %d", len(rows))
	}
	// ordenado por tempo ascendente: a mais antiga é do mês anterior
	if rows[0].SessionID != "b" || rows[2].SessionID != "a" || rows[2].Source != "main" {
		t.Fatalf("ordenação/parse errado: [%+v] [%+v] [%+v]", rows[0], rows[1], rows[2])
	}
	// linhas antes de `since` são filtradas
	recent, err := Store{Root: root}.Usage(now.Add(-12 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("filtro por since errado, got %d: %+v", len(recent), recent)
	}
	for _, r := range recent {
		if r.SessionID != "a" {
			t.Fatalf("linha fora de `since` incluída: %+v", r)
		}
	}
}

func TestUsageOnlyLoadsNeededMonths(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	old := now.AddDate(0, -3, 0)
	writeFile(t, filepath.Join(root, "usage", "token-usage-"+old.Format("2006-01")+".jsonl"),
		`{"timestamp":"`+old.Format(time.RFC3339)+`","sessionId":"x","model":"m","source":"main","inputTokens":1,"outputTokens":1,"totalTokens":2}`+"\n",
	)
	files := Store{Root: root}.usageFiles(now.AddDate(0, 0, -5))
	if len(files) != 0 {
		t.Fatalf("arquivo de 3 meses atrás não deveria ser listado: %v", files)
	}
}

func TestAggregateSplitsMainSubDaysModels(t *testing.T) {
	d1 := time.Date(2026, 10, 5, 10, 0, 0, 0, time.Local)
	d2 := time.Date(2026, 10, 6, 10, 0, 0, 0, time.Local)
	rows := []TokenUsage{
		{Timestamp: d1, SessionID: "a", Model: "m1", Source: "main", InputTokens: 100, OutputTokens: 10, TotalTokens: 110, APIDurationMs: 1000},
		{Timestamp: d1, SessionID: "a", Model: "m1", Source: "Explore", InputTokens: 50, OutputTokens: 5, TotalTokens: 55, APIDurationMs: 500},
		{Timestamp: d2, SessionID: "b", Model: "m2", Source: "main", InputTokens: 200, OutputTokens: 20, TotalTokens: 220, APIDurationMs: 2000},
	}
	a := Aggregate(rows)
	if a.All.Requests != 3 || a.All.Total != 385 || a.All.LatencyMs != 3500 {
		t.Fatalf("total errado: %+v", a.All)
	}
	if a.Main.Requests != 2 || a.Main.Total != 330 {
		t.Fatalf("main errado: %+v", a.Main)
	}
	if a.Sub.Requests != 1 || a.Sub.Total != 55 {
		t.Fatalf("sub errado: %+v", a.Sub)
	}
	if len(a.ByDay) != 2 || a.ByDay[d1.Format("2006-01-02")].Total != 165 {
		t.Fatalf("byDay errado: %+v", a.ByDay)
	}
	if len(a.ByModel) != 2 || a.ByModel["m1"].Total != 165 || a.ByModel["m2"].Total != 220 {
		t.Fatalf("byModel errado: %+v", a.ByModel)
	}
	if !a.Since.Equal(d1) || !a.Until.Equal(d2) {
		t.Fatalf("since/until errado: %v → %v", a.Since, a.Until)
	}
}

func TestRecordsOrderAndTotals(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "usage_record.jsonl"),
		`{"version":1,"sessionId":"old","timestamp":1700000200000,"startTime":1700000100000,"project":"/tmp/oldproj","durationMs":100000,"totalLatencyMs":50000,"models":{"m1":{"requests":2,"inputTokens":100,"outputTokens":10,"totalTokens":110,"totalLatencyMs":50000}},"tools":{"totalCalls":3,"totalSuccess":3,"totalFail":0},"files":{"linesAdded":5,"linesRemoved":1}}`+"\n"+
			`{"version":1,"sessionId":"new","timestamp":1700000500000,"startTime":1700000400000,"project":"/tmp/newproj","durationMs":100000,"totalLatencyMs":50000,"models":{"m2":{"requests":1,"inputTokens":50,"outputTokens":5,"totalTokens":55,"totalLatencyMs":50000}},"tools":{"totalCalls":1,"totalSuccess":0,"totalFail":1},"files":{"linesAdded":0,"linesRemoved":0}}`+"\n",
	)
	recs, err := Store{Root: root}.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].SessionID != "new" || recs[1].SessionID != "old" {
		t.Fatalf("ordenação errada: %+v", recs)
	}
	if recs[0].TotalTokens != 55 || recs[0].ToolFails != 1 {
		t.Fatalf("totais do new errados: %+v", recs[0])
	}
	if recs[1].TotalTokens != 110 || recs[1].LinesAdded != 5 {
		t.Fatalf("totais do old errados: %+v", recs[1])
	}
	pm, err := Store{Root: root}.ProjectBySession()
	if err != nil {
		t.Fatal(err)
	}
	if pm["new"] != "/tmp/newproj" || pm["old"] != "/tmp/oldproj" {
		t.Fatalf("project map errado: %v", pm)
	}
}

func TestHumanIntAndDur(t *testing.T) {
	cases := map[string]struct {
		in   any
		want string
	}{
		"int0":   {int64(0), "0"},
		"int999": {int64(999), "999"},
		"int1K":  {int64(1000), "1K"},
		"int1M":  {int64(1_000_000), "1M"},
		"int5M":  {int64(5_000_000), "5M"},
	}
	for name, c := range cases {
		if got := humanInt(c.in.(int64)); got != c.want {
			t.Errorf("%s: humanInt(%v) = %q, want %q", name, c.in, got, c.want)
		}
	}
	if got := humanDur(500); got != "500ms" {
		t.Errorf("humanDur(500) = %q", got)
	}
	if got := humanDur(90_000); got != "1m30s" {
		t.Errorf("humanDur(90000) = %q", got)
	}
	if got := humanDur(155_000); got != "2m35s" {
		t.Errorf("humanDur(155000) = %q", got)
	}
	if got := humanDur(3*3600_000 + 12*60_000); got != "3h12m" {
		t.Errorf("humanDur(3h12m) = %q", got)
	}
}

func TestTokensBySessionTopModel(t *testing.T) {
	now := time.Now()
	rows := []TokenUsage{
		{Timestamp: now, SessionID: "a", Model: "m1", Source: "main", TotalTokens: 100},
		{Timestamp: now, SessionID: "a", Model: "m2", Source: "main", TotalTokens: 300},
		{Timestamp: now, SessionID: "b", Model: "m1", Source: "main", TotalTokens: 10},
	}
	per := tokensBySession(rows)
	if per["a"].Tokens != 400 || per["a"].Reqs != 2 || per["a"].TopModel != "m2" {
		t.Fatalf("sessão a errada: %+v", per["a"])
	}
	if per["b"].TopModel != "m1" {
		t.Fatalf("sessão b errada: %+v", per["b"])
	}
}

func TestSplitCommand(t *testing.T) {
	// sem args: comando padrão, sem panico em slice
	if cmd, args := splitCommand(nil); cmd != "status" || len(args) != 0 {
		t.Fatalf("splitCommand(nil) = %q, %v", cmd, args)
	}
	if cmd, args := splitCommand([]string{}); cmd != "status" || len(args) != 0 {
		t.Fatalf("splitCommand([]) = %q, %v", cmd, args)
	}
	if cmd, args := splitCommand([]string{"live"}); cmd != "live" || len(args) != 0 {
		t.Fatalf("splitCommand(live) = %q, %v", cmd, args)
	}
	if cmd, args := splitCommand([]string{"usage", "-days", "3"}); cmd != "usage" || len(args) != 2 || args[1] != "3" {
		t.Fatalf("splitCommand(usage -days 3) = %q, %v", cmd, args)
	}
}

func TestShortPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, "projetos", "x")
	if got := shortPath(p); !strings.HasPrefix(got, "~/") {
		t.Errorf("shortPath(%q) = %q, queria ~", p, got)
	}
	if got := shortPath(""); got != "-" {
		t.Errorf("shortPath(\"\") = %q", got)
	}
}
