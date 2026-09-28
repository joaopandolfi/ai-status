// ai-status: live terminal dashboard for the SGLang server configured in ~/.qwen/settings.json.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// metrics maps metric name (labels stripped) to the sum over all label sets.
// Series with a mode="x" label are also stored under "name:x".
type metrics map[string]float64

func parse(r io.Reader) metrics {
	m := metrics{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		sp := strings.LastIndexByte(line, ' ')
		if sp < 0 {
			continue
		}
		v, err := strconv.ParseFloat(line[sp+1:], 64)
		if err != nil || math.IsNaN(v) {
			continue
		}
		name, labels, _ := strings.Cut(line[:sp], "{")
		name = strings.TrimPrefix(name, "sglang:")
		m[name] += v
		if _, mode, ok := strings.Cut(labels, `mode="`); ok {
			mode, _, _ = strings.Cut(mode, `"`)
			m[name+":"+mode] += v
		}
	}
	return m
}

type sample struct {
	t time.Time
	m metrics
}

// baseURL finds the SGLang provider in qwen-code settings: the active model first, else anything named sglang.
func baseURL() string {
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".qwen", "settings.json"))
	if err != nil {
		return ""
	}
	var s struct {
		Model          struct{ Name string } `json:"model"`
		ModelProviders map[string][]struct {
			ID, Name, Description, BaseURL string
		} `json:"modelProviders"`
	}
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	fallback := ""
	for _, ps := range s.ModelProviders {
		for _, p := range ps {
			isSG := strings.Contains(strings.ToLower(p.Name+p.Description), "sglang")
			if isSG && p.ID == s.Model.Name {
				return strings.TrimSuffix(strings.TrimSuffix(p.BaseURL, "/"), "/v1")
			}
			if isSG && fallback == "" {
				fallback = strings.TrimSuffix(strings.TrimSuffix(p.BaseURL, "/"), "/v1")
			}
		}
	}
	return fallback
}

const (
	reset = "\x1b[0m"
	bold  = "\x1b[1m"
	dim   = "\x1b[2m"
	red   = "\x1b[31m"
	green = "\x1b[32m"
	yel   = "\x1b[33m"
	cyan  = "\x1b[36m"
)

func spark(xs []float64) string {
	const bars = "▁▂▃▄▅▆▇█"
	mx := 0.0
	for _, x := range xs {
		mx = math.Max(mx, x)
	}
	var sb strings.Builder
	r := []rune(bars)
	for _, x := range xs {
		i := 0
		if mx > 0 {
			i = int(x / mx * float64(len(r)-1))
		}
		sb.WriteRune(r[i])
	}
	return sb.String()
}

func gauge(frac float64, width int) string {
	frac = math.Max(0, math.Min(1, frac))
	n := int(frac*float64(width) + 0.5)
	c := green
	if frac > 0.9 {
		c = red
	} else if frac > 0.7 {
		c = yel
	}
	return c + strings.Repeat("█", n) + dim + strings.Repeat("░", width-n) + reset
}

func human(x float64) string {
	switch {
	case x >= 1e9:
		return fmt.Sprintf("%.2fG", x/1e9)
	case x >= 1e6:
		return fmt.Sprintf("%.2fM", x/1e6)
	case x >= 1e3:
		return fmt.Sprintf("%.1fk", x/1e3)
	}
	return fmt.Sprintf("%.0f", x)
}

// avg returns delta(sum)/delta(count) for a histogram between two samples, or the lifetime average if no new events.
func avg(a, b metrics, name string) float64 {
	dc := b[name+"_count"] - a[name+"_count"]
	if dc > 0 {
		return (b[name+"_sum"] - a[name+"_sum"]) / dc
	}
	if c := b[name+"_count"]; c > 0 {
		return b[name+"_sum"] / c
	}
	return 0
}

func ms(sec float64) string {
	if sec >= 1 {
		return fmt.Sprintf("%.2fs", sec)
	}
	return fmt.Sprintf("%.0fms", sec*1000)
}

func main() {
	url := flag.String("url", "", "SGLang base URL (default: read from ~/.qwen/settings.json)")
	every := flag.Duration("i", time.Second, "poll interval")
	window := flag.Int("w", 5, "rate window, in samples")
	flag.Parse()
	if *url == "" {
		*url = baseURL()
	}
	if *url == "" {
		fmt.Fprintln(os.Stderr, "no SGLang provider in ~/.qwen/settings.json; pass -url http://host:port")
		os.Exit(1)
	}

	fmt.Print("\x1b[?1049h\x1b[?25l")
	restore := func() { fmt.Print("\x1b[?25h\x1b[?1049l") }
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { <-sig; restore(); os.Exit(0) }()
	defer restore()

	client := &http.Client{Timeout: 3 * time.Second}
	const histLen = 60
	var samples []sample
	var genHist, runHist, kvHist []float64
	push := func(h []float64, v float64) []float64 {
		h = append(h, v)
		if len(h) > histLen {
			h = h[1:]
		}
		return h
	}

	for tick := time.Tick(*every); ; <-tick {
		resp, err := client.Get(*url + "/metrics")
		var out strings.Builder
		out.WriteString("\x1b[H\x1b[2J")
		fmt.Fprintf(&out, "%sSGLang%s %s  %s%s%s\n\n", bold, reset, *url, dim, time.Now().Format("15:04:05"), reset)
		if err != nil || resp.StatusCode != 200 {
			if err == nil {
				resp.Body.Close()
				err = fmt.Errorf("HTTP %d (server started with --enable-metrics?)", resp.StatusCode)
			}
			fmt.Fprintf(&out, "%serror: %v%s\n", red, err, reset)
			fmt.Print(out.String())
			continue
		}
		m := parse(resp.Body)
		resp.Body.Close()

		samples = append(samples, sample{time.Now(), m})
		if len(samples) > *window+1 {
			samples = samples[1:]
		}
		old := samples[0]
		dt := time.Since(old.t).Seconds()
		rate := func(name string) float64 {
			if dt <= 0 {
				return 0
			}
			return math.Max(0, (m[name]-old.m[name])/dt)
		}

		// realtime_tokens_total ticks during generation; generation_tokens_total only when a request finishes.
		genTps := rate("realtime_tokens_total:decode")
		promptTps := rate("realtime_tokens_total:prefill_compute")
		cachedTps := rate("realtime_tokens_total:prefill_cache")
		kvFrac := 0.0
		if m["max_total_num_tokens"] > 0 {
			kvFrac = m["kv_used_tokens"] / m["max_total_num_tokens"]
		}
		genHist = push(genHist, genTps)
		runHist = push(runHist, m["num_running_reqs"])
		kvHist = push(kvHist, kvFrac)

		row := func(label, val string) { fmt.Fprintf(&out, "  %-18s %s\n", label, val) }
		section := func(s string) { fmt.Fprintf(&out, "%s%s%s\n", cyan+bold, s, reset) }

		section("Throughput")
		row("gen tok/s", fmt.Sprintf("%s%8.1f%s  %s", bold, genTps, reset, spark(genHist)))
		row("prefill tok/s", fmt.Sprintf("%8.1f  %s+ %.1f from prefix cache%s", promptTps, dim, cachedTps, reset))
		row("engine gen tok/s", fmt.Sprintf("%8.1f  %s(scheduler gauge)%s", m["gen_throughput"], dim, reset))
		row("req/s", fmt.Sprintf("%8.2f", rate("num_requests_total")))
		out.WriteString("\n")

		section("Sessions")
		row("running", fmt.Sprintf("%s%8.0f%s  %s", bold, m["num_running_reqs"], reset, spark(runHist)))
		row("queued", fmt.Sprintf("%8.0f  grammar %.0f  paused %.0f  retracted %.0f",
			m["num_queue_reqs"], m["num_grammar_queue_reqs"], m["num_paused_reqs"], m["num_retracted_reqs"]))
		row("http active", fmt.Sprintf("%8.0f", m["http_requests_active"]))
		row("utilization", fmt.Sprintf("%7.1f%%", m["utilization"]*100))
		out.WriteString("\n")

		section("KV cache")
		row("used", fmt.Sprintf("%s %5.1f%%  %s / %s tok", gauge(kvFrac, 30), kvFrac*100,
			human(m["kv_used_tokens"]), human(m["max_total_num_tokens"])))
		row("history", spark(kvHist))
		row("evictable", human(m["kv_evictable_tokens"])+" tok (radix cache)")
		row("prefix hit rate", fmt.Sprintf("%.1f%%", m["cache_hit_rate"]*100))
		if m["mamba_available_tokens"]+m["mamba_used_tokens"] > 0 {
			row("mamba state", fmt.Sprintf("%s %5.1f%%", gauge(m["mamba_usage"], 30), m["mamba_usage"]*100))
		}
		row("decode ctx sum", human(m["decode_sum_seq_lens"])+" tok")
		out.WriteString("\n")

		section("Latency (window avg)")
		row("TTFT", ms(avg(old.m, m, "time_to_first_token_seconds")))
		row("e2e request", ms(avg(old.m, m, "e2e_request_latency_seconds")))
		row("queue time", ms(avg(old.m, m, "queue_time_seconds")))
		out.WriteString("\n")

		section("Speculative decoding")
		row("accept length", fmt.Sprintf("%.2f", m["spec_accept_length"]))
		row("accept rate", fmt.Sprintf("%.1f%%", m["spec_accept_rate"]*100))
		out.WriteString("\n")

		section("Totals")
		row("requests", human(m["num_requests_total"]))
		row("prompt tokens", human(m["prompt_tokens_total"]))
		row("gen tokens", human(m["generation_tokens_total"]))
		row("memory GB", fmt.Sprintf("weights %.1f  kv %.1f  graph %.1f",
			m["weight_memory_usage_gb"], m["kv_cache_memory_usage_gb"], m["graph_memory_usage_gb"]))
		row("context len", human(m["context_len"]))
		fmt.Fprintf(&out, "\n%sctrl-c to quit%s\n", dim, reset)
		fmt.Print(out.String())
	}
}
