package main

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	m := parse(strings.NewReader(`# HELP x
sglang:generation_tokens_total{is_streaming="false"} 8.0
sglang:generation_tokens_total{is_streaming="true"} 2.0
sglang:fwd_occupancy NaN
sglang:token_usage 0.5
`))
	if m["generation_tokens_total"] != 10 || m["token_usage"] != 0.5 {
		t.Fatal(m)
	}
	if _, ok := m["fwd_occupancy"]; ok {
		t.Fatal("NaN kept")
	}
}

func TestParseMode(t *testing.T) {
	m := parse(strings.NewReader(`sglang:realtime_tokens_total{engine_type="unified",mode="decode",tp_rank="0"} 44.0
sglang:realtime_tokens_total{mode="prefill_compute"} 6
`))
	if m["realtime_tokens_total:decode"] != 44 || m["realtime_tokens_total"] != 50 {
		t.Fatal(m)
	}
}

func TestDedup(t *testing.T) {
	got := dedup([]string{"http://h:1/v1", "http://h:1/", " ", "http://h:2/v1"})
	if len(got) != 2 || got[0] != "http://h:1" || got[1] != "http://h:2" {
		t.Fatal(got)
	}
}

func TestDrawClamp(t *testing.T) {
	body := "a\nb\nc\nd\ne\n"
	if s := draw("h\n", body, 99, 4); s != 3 { // 4 rows - 1 head - 1 indicator = 2 visible
		t.Fatal(s)
	}
	if s := draw("h\n", body, -5, 4); s != 0 {
		t.Fatal(s)
	}
	if s := draw("h\n", body, 3, 0); s != 0 { // unknown height: no clipping
		t.Fatal(s)
	}
}

func TestSplitKeys(t *testing.T) {
	got := strings.Join(splitKeys("\x1b[B\x1b[Bq\x1b[6~2"), "|")
	if got != "\x1b[B|\x1b[B|q|\x1b[6~|2" {
		t.Fatalf("%q", got)
	}
}
