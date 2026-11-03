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
	"os/exec"
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

// extraProviders are always offered besides the SGLang providers found in ~/.qwen/settings.json.
var extraProviders = []string{"http://10.162.1.23:11436/v1"}

func trimURL(u string) string {
	return strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(u), "/"), "/v1")
}

// providers lists SGLang base URLs: the active qwen-code model first, then other sglang providers, then extraProviders.
func providers() []string {
	var urls []string
	home, _ := os.UserHomeDir()
	if b, err := os.ReadFile(filepath.Join(home, ".qwen", "settings.json")); err == nil {
		var s struct {
			Model          struct{ Name string } `json:"model"`
			ModelProviders map[string][]struct {
				ID, Name, Description, BaseURL string
			} `json:"modelProviders"`
		}
		if json.Unmarshal(b, &s) == nil {
			for _, ps := range s.ModelProviders {
				for _, p := range ps {
					if !strings.Contains(strings.ToLower(p.Name+p.Description), "sglang") {
						continue
					}
					if p.ID == s.Model.Name {
						urls = append([]string{p.BaseURL}, urls...)
					} else {
						urls = append(urls, p.BaseURL)
					}
				}
			}
		}
	}
	return dedup(append(urls, extraProviders...))
}

func dedup(urls []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, u := range urls {
		if u = trimURL(u); u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// keys puts the terminal in cbreak mode and sends each keypress; arrows arrive as "left"/"right".
func keys() (<-chan string, func()) {
	stty := func(args ...string) string {
		c := exec.Command("stty", args...)
		c.Stdin = os.Stdin
		b, _ := c.Output()
		return strings.TrimSpace(string(b))
	}
	saved := stty("-g")
	stty("-icanon", "-echo", "min", "1")
	ch := make(chan string)
	go func() {
		names := map[string]string{
			"\x1b[C": "right", "\x1b[D": "left", "\x1b[A": "up", "\x1b[B": "down", "\x1b[5~": "pgup", "\x1b[6~": "pgdn",
			"k": "up", "j": "down", "b": "pgup", " ": "pgdn",
		}
		buf := make([]byte, 64)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			// key repeat can deliver several keys in one read
			for _, k := range splitKeys(string(buf[:n])) {
				if name, ok := names[k]; ok {
					k = name
				}
				ch <- k
			}
		}
	}()
	return ch, func() {
		if saved != "" {
			stty(saved)
		}
	}
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
	urlFlag := flag.String("url", "", "SGLang base URLs, comma-separated (default: ~/.qwen/settings.json + extras)")
	every := flag.Duration("i", time.Second, "poll interval")
	window := flag.Int("w", 5, "rate window, in samples")
	flag.Parse()
	urls := providers()
	if *urlFlag != "" {
		urls = dedup(strings.Split(*urlFlag, ","))
	}
	if len(urls) == 0 {
		fmt.Fprintln(os.Stderr, "no SGLang provider in ~/.qwen/settings.json; pass -url http://host:port")
		os.Exit(1)
	}

	keyCh, restoreTTY := keys()
	fmt.Print("\x1b[?1049h\x1b[?25l")
	restore := func() { fmt.Print("\x1b[?25h\x1b[?1049l"); restoreTTY() }
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

	sel, scroll := 0, 0
	fetch := true
	var head, body strings.Builder
	tick := time.Tick(*every)
	for {
		if fetch {
			head.Reset()
			body.Reset()
			url := urls[sel]
			resp, err := client.Get(url + "/metrics")
			for i, u := range urls {
				if i == sel {
					fmt.Fprintf(&head, "\x1b[7m%s %d %s %s ", bold, i+1, u, reset)
				} else {
					fmt.Fprintf(&head, "%s %d %s %s", dim, i+1, u, reset)
				}
			}
			fmt.Fprintf(&head, "\n%sSGLang%s  %s%s  [1-%d / tab / ←→ switch, ↑↓ PgUp PgDn scroll, q quit]%s\n",
				bold, reset, dim, time.Now().Format("15:04:05"), len(urls), reset)
			if err != nil || resp.StatusCode != 200 {
				if err == nil {
					resp.Body.Close()
					err = fmt.Errorf("HTTP %d (server started with --enable-metrics?)", resp.StatusCode)
				}
				fmt.Fprintf(&body, "%serror: %v%s\n", red, err, reset)
			} else {
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
				kvFrac := 0.0
				if m["max_total_num_tokens"] > 0 {
					kvFrac = m["kv_used_tokens"] / m["max_total_num_tokens"]
				}
				// realtime_tokens_total ticks during generation; generation_tokens_total only when a request finishes.
				genTps := rate("realtime_tokens_total:decode")
				genHist = push(genHist, genTps)
				runHist = push(runHist, m["num_running_reqs"])
				kvHist = push(kvHist, kvFrac)
				render(&body, m, old.m, rate, genTps, kvFrac, genHist, runHist, kvHist)
			}
		}
		scroll = draw(head.String(), body.String(), scroll, termRows())

		fetch = false
		select {
		case <-tick:
			fetch = true
		case k := <-keyCh:
			next := sel
			switch {
			case k == "q":
				return
			case k == "\t" || k == "right":
				next = (sel + 1) % len(urls)
			case k == "left":
				next = (sel + len(urls) - 1) % len(urls)
			case len(k) == 1 && k[0] >= '1' && int(k[0]-'1') < len(urls):
				next = int(k[0] - '1')
			case k == "up":
				scroll--
			case k == "down":
				scroll++
			case k == "pgup":
				scroll -= 10
			case k == "pgdn":
				scroll += 10
			}
			if next != sel {
				sel, scroll, fetch = next, 0, true
				samples, genHist, runHist, kvHist = nil, nil, nil, nil
			}
		}
	}
}

// splitKeys splits raw input into keys; escape sequences end at their first letter or '~'.
func splitKeys(s string) []string {
	var keys []string
	for len(s) > 0 {
		n := 1
		if strings.HasPrefix(s, "\x1b[") {
			n = strings.IndexAny(s[2:], "ABCDEFGHJKPQRS~") + 3
			if n < 3 {
				n = len(s)
			}
		}
		keys = append(keys, s[:n])
		s = s[n:]
	}
	return keys
}

// termRows returns the terminal height, or 0 if unknown.
func termRows() int {
	c := exec.Command("stty", "size")
	c.Stdin = os.Stdin
	b, _ := c.Output()
	var rows, cols int
	fmt.Sscan(string(b), &rows, &cols)
	return rows
}

// draw keeps head pinned and shows the body window starting at scroll; returns the clamped scroll.
func draw(head, body string, scroll, rows int) int {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	avail := len(lines)
	if rows > 0 {
		avail = max(1, rows-strings.Count(head, "\n")-1)
	}
	scroll = max(0, min(scroll, len(lines)-avail))
	end := min(len(lines), scroll+avail)
	var out strings.Builder
	out.WriteString("\x1b[H\x1b[2J" + head)
	if scroll > 0 {
		fmt.Fprintf(&out, "%s↑ %d more%s", dim, scroll, reset)
	}
	if end < len(lines) {
		fmt.Fprintf(&out, "%s  ↓ %d more%s", dim, len(lines)-end, reset)
	}
	out.WriteString("\n" + strings.Join(lines[scroll:end], "\n"))
	fmt.Print(out.String())
	return scroll
}

func render(out *strings.Builder, m, old metrics, rate func(string) float64, genTps, kvFrac float64, genHist, runHist, kvHist []float64) {
	row := func(label, val string) { fmt.Fprintf(out, "  %-18s %s\n", label, val) }
	section := func(s string) { fmt.Fprintf(out, "%s%s%s\n", cyan+bold, s, reset) }

	section("Throughput")
	row("gen tok/s", fmt.Sprintf("%s%8.1f%s  %s", bold, genTps, reset, spark(genHist)))
	row("prefill tok/s", fmt.Sprintf("%8.1f  %s+ %.1f from prefix cache%s",
		rate("realtime_tokens_total:prefill_compute"), dim, rate("realtime_tokens_total:prefill_cache"), reset))
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
	row("TTFT", ms(avg(old, m, "time_to_first_token_seconds")))
	row("e2e request", ms(avg(old, m, "e2e_request_latency_seconds")))
	row("queue time", ms(avg(old, m, "queue_time_seconds")))
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
}
