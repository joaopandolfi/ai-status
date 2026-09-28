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
