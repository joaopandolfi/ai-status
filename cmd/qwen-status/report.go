// qwen-status: agregação e renderização dos relatórios.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// Totals é a soma do uso de um conjunto de requests.
type Totals struct {
	Requests  int   `json:"requests"`
	Input     int64 `json:"inputTokens"`
	Output    int64 `json:"outputTokens"`
	Cached    int64 `json:"cachedTokens"`
	Thoughts  int64 `json:"thoughtsTokens"`
	Total     int64 `json:"totalTokens"`
	LatencyMs int64 `json:"totalLatencyMs"`
}

func (t Totals) Add(r TokenUsage) Totals {
	return Totals{
		Requests:  t.Requests + 1,
		Input:     t.Input + r.InputTokens,
		Output:    t.Output + r.OutputTokens,
		Cached:    t.Cached + r.CachedTokens,
		Thoughts:  t.Thoughts + r.ThoughtsTokens,
		Total:     t.Total + r.TotalTokens,
		LatencyMs: t.LatencyMs + r.APIDurationMs,
	}
}

// UsageAgg agrega linhas de uso: total, main vs subagente, por dia e por modelo.
type UsageAgg struct {
	Since   time.Time         `json:"since"`
	Until   time.Time         `json:"until"`
	All     Totals            `json:"total"`
	Main    Totals            `json:"main"`
	Sub     Totals            `json:"sub"`
	ByDay   map[string]Totals `json:"byDay"`
	ByModel map[string]Totals `json:"byModel"`
}

// Aggregate soma as linhas em todos os cortes.
func Aggregate(rows []TokenUsage) *UsageAgg {
	a := &UsageAgg{ByDay: map[string]Totals{}, ByModel: map[string]Totals{}}
	var min, max time.Time
	for i, r := range rows {
		if i == 0 || r.Timestamp.Before(min) {
			min = r.Timestamp
		}
		if i == 0 || r.Timestamp.After(max) {
			max = r.Timestamp
		}
		a.All = a.All.Add(r)
		if r.Source == "main" {
			a.Main = a.Main.Add(r)
		} else {
			a.Sub = a.Sub.Add(r)
		}
		a.ByDay[r.Timestamp.Format("2006-01-02")] = a.ByDay[r.Timestamp.Format("2006-01-02")].Add(r)
		a.ByModel[r.Model] = a.ByModel[r.Model].Add(r)
	}
	if len(rows) > 0 {
		a.Since, a.Until = min, max
	}
	return a
}

// sessionTokens resume o uso de uma sessão a partir das linhas.
type sessionTokens struct {
	Tokens   int64
	Reqs     int
	TopModel string
}

func tokensBySession(rows []TokenUsage) map[string]sessionTokens {
	perModel := map[string]map[string]int64{}
	out := map[string]sessionTokens{}
	for _, r := range rows {
		s := out[r.SessionID]
		s.Tokens += r.TotalTokens
		s.Reqs++
		if perModel[r.SessionID] == nil {
			perModel[r.SessionID] = map[string]int64{}
		}
		perModel[r.SessionID][r.Model] += r.TotalTokens
		out[r.SessionID] = s
	}
	for id, models := range perModel {
		var top string
		var best int64
		for m, t := range models {
			if t > best {
				top, best = m, t
			}
		}
		s := out[id]
		s.TopModel = top
		out[id] = s
	}
	return out
}

// ---------- relatórios ----------

// Overview é a tela padrão: agentes vivos + uso de hoje.
func Overview(w io.Writer, d Store, now time.Time) error {
	live, err := d.Live()
	if err != nil {
		return err
	}
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	rows, err := d.Usage(startOfMonth)
	if err != nil {
		return err
	}
	today := now.Format("2006-01-02")
	var todayRows []TokenUsage
	for _, r := range rows {
		if r.Timestamp.Format("2006-01-02") == today {
			todayRows = append(todayRows, r)
		}
	}

	fmt.Fprintf(w, "qwen-status · %s\n\n", now.Format("2006-01-02 15:04:05"))

	if len(live) == 0 {
		fmt.Fprintln(w, "agentes vivos: nenhum")
	} else {
		fmt.Fprintf(w, "agentes vivos (%d)\n", len(live))
		tw := tabwriter.NewWriter(w, 2, 4, 1, ' ', 0)
		fmt.Fprintln(tw, "  nome\tprojeto\thá\tqwen\ttipo\ttokens (sessão)")
		perSession := tokensBySession(rows)
		for _, ls := range live {
			st := perSession[ls.SessionID]
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n",
				ls.Name, shortPath(ls.CWD),
				humanDur(now.Sub(time.UnixMilli(ls.StartedAt)).Milliseconds()),
				ls.QwenVersion, ls.Kind, humanInt(st.Tokens))
		}
		tw.Flush()
	}

	fmt.Fprintf(w, "\nusos de hoje (%s)\n", today)
	renderAgg(w, Aggregate(todayRows))
	return nil
}

// LiveReport detalha os agentes vivos com uso desde o início da sessão.
func LiveReport(w io.Writer, d Store, now time.Time) error {
	live, err := d.Live()
	if err != nil {
		return err
	}
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	rows, err := d.Usage(startOfMonth)
	if err != nil {
		return err
	}
	perSession := tokensBySession(rows)
	if len(live) == 0 {
		fmt.Fprintln(w, "nenhum agente vivo")
		return nil
	}
	tw := tabwriter.NewWriter(w, 2, 4, 1, ' ', 0)
	fmt.Fprintln(tw, "  nome\tprojeto\thá\ttipo\ttokens\treqs\tmodelo")
	for _, ls := range live {
		st := perSession[ls.SessionID]
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			ls.Name, shortPath(ls.CWD),
			humanDur(now.Sub(time.UnixMilli(ls.StartedAt)).Milliseconds()),
			ls.Kind, humanInt(st.Tokens), st.Reqs, st.TopModel)
	}
	return tw.Flush()
}

// UsageReport agrega o período pedido e devolve a agregação (para -json).
func UsageReport(w io.Writer, d Store, now time.Time, days int, model, project string) (*UsageAgg, error) {
	sinceDay := now.AddDate(0, 0, -(days - 1))
	since := time.Date(sinceDay.Year(), sinceDay.Month(), sinceDay.Day(), 0, 0, 0, 0, sinceDay.Location())
	rows, err := d.Usage(since)
	if err != nil {
		return nil, err
	}
	var filtered []TokenUsage
	for _, r := range rows {
		if model != "" && r.Model != model {
			continue
		}
		filtered = append(filtered, r)
	}
	if project != "" {
		pm, err := d.ProjectBySession()
		if err != nil {
			return nil, err
		}
		var pf []TokenUsage
		for _, r := range filtered {
			p := pm[r.SessionID]
			if p == project || filepath.Base(p) == project {
				pf = append(pf, r)
			}
		}
		filtered = pf
	}
	agg := Aggregate(filtered)
	var header string
	if days <= 1 {
		header = "hoje (" + now.Format("2006-01-02") + ")"
	} else {
		header = fmt.Sprintf("últimos %d dias (%s → %s)", days, since.Format("2006-01-02"), now.Format("2006-01-02"))
	}
	fmt.Fprintf(w, "período: %s\n\n", header)
	renderAgg(w, agg)
	fmt.Fprintf(w, "\npor dia\n")
	tw := tabwriter.NewWriter(w, 2, 4, 1, ' ', 0)
	fmt.Fprintln(tw, "  data\treqs\tinput\toutput\tcached\ttotal\tlatência")
	for _, day := range sortedKeys(agg.ByDay) {
		t := agg.ByDay[day]
		fmt.Fprintf(tw, "  %s\t%d\t%s\t%s\t%s\t%s\t%s\n", day, t.Requests, humanInt(t.Input), humanInt(t.Output), humanInt(t.Cached), humanInt(t.Total), humanDur(t.LatencyMs))
	}
	tw.Flush()
	fmt.Fprintln(w, "\npor modelo")
	tw = tabwriter.NewWriter(w, 2, 4, 1, ' ', 0)
	fmt.Fprintln(tw, "  modelo\treqs\tinput\toutput\tcached\ttotal\tlatência")
	for _, m := range sortedKeys(agg.ByModel) {
		t := agg.ByModel[m]
		fmt.Fprintf(tw, "  %s\t%d\t%s\t%s\t%s\t%s\t%s\n", m, t.Requests, humanInt(t.Input), humanInt(t.Output), humanInt(t.Cached), humanInt(t.Total), humanDur(t.LatencyMs))
	}
	return agg, tw.Flush()
}

// SessionsReport lista sessões finalizadas recentes e devolve os registros (para -json).
func SessionsReport(w io.Writer, d Store, limit int) ([]SessionRecord, error) {
	recs, err := d.Records()
	if err != nil {
		return nil, err
	}
	if limit > 0 && limit < len(recs) {
		recs = recs[:limit]
	}
	if len(recs) == 0 {
		fmt.Fprintln(w, "nenhuma sessão registrada")
		return recs, nil
	}
	tw := tabwriter.NewWriter(w, 2, 4, 1, ' ', 0)
	fmt.Fprintln(tw, "  sessão\tinício\tprojeto\tduração\treqs\ttokens\ttools\tarquivos\tmodelos")
	for _, r := range recs {
		tools := strconv.Itoa(r.ToolCalls)
		if r.ToolFails > 0 {
			tools += fmt.Sprintf(" (%d✗)", r.ToolFails)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%d\t%s\t%s\t+%d -%d\t%s\n",
			shortID(r.SessionID), r.StartTime.Format("01-02 15:04"), filepath.Base(r.Project),
			humanDur(r.DurationMs), r.TotalRequests, humanInt(r.TotalTokens), tools,
			r.LinesAdded, r.LinesRemoved, strings.Join(modelNames(r.Models), ","))
	}
	return recs, tw.Flush()
}

// ---------- helpers de render ----------

func renderAgg(w io.Writer, a *UsageAgg) {
	if a.All.Requests == 0 {
		fmt.Fprintln(w, "  (sem uso registrado)")
		return
	}
	tw := tabwriter.NewWriter(w, 2, 4, 1, ' ', 0)
	fmt.Fprintln(tw, "  \treqs\tinput\toutput\tcached\tthoughts\ttotal\tlatência")
	fmt.Fprintf(tw, "  main\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n", a.Main.Requests, humanInt(a.Main.Input), humanInt(a.Main.Output), humanInt(a.Main.Cached), humanInt(a.Main.Thoughts), humanInt(a.Main.Total), humanDur(a.Main.LatencyMs))
	fmt.Fprintf(tw, "  sub\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n", a.Sub.Requests, humanInt(a.Sub.Input), humanInt(a.Sub.Output), humanInt(a.Sub.Cached), humanInt(a.Sub.Thoughts), humanInt(a.Sub.Total), humanDur(a.Sub.LatencyMs))
	fmt.Fprintf(tw, "  total\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n", a.All.Requests, humanInt(a.All.Input), humanInt(a.All.Output), humanInt(a.All.Cached), humanInt(a.All.Thoughts), humanInt(a.All.Total), humanDur(a.All.LatencyMs))
	tw.Flush()
	if len(a.ByModel) > 0 {
		var parts []string
		for _, m := range sortedKeys(a.ByModel) {
			t := a.ByModel[m]
			parts = append(parts, fmt.Sprintf("%s %d req · %s tok", m, t.Requests, humanInt(t.Total)))
		}
		fmt.Fprintf(w, "  modelos: %s\n", strings.Join(parts, " | "))
	}
}

func sortedKeys(m map[string]Totals) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func modelNames(m map[string]ModelStats) []string {
	var out []string
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// shortPath encurta paths com home para ~.
func shortPath(p string) string {
	if p == "" {
		return "-"
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		p = "~" + p[len(home):]
	}
	return p
}

func humanInt(n int64) string {
	f := func(v float64, suf string) string {
		s := strconv.FormatFloat(v, 'f', 1, 64)
		if strings.HasSuffix(s, ".0") {
			s = s[:len(s)-2]
		}
		return s + suf
	}
	switch {
	case n >= 1e9:
		return f(float64(n)/1e9, "B")
	case n >= 1e6:
		return f(float64(n)/1e6, "M")
	case n >= 1e3:
		return f(float64(n)/1e3, "K")
	}
	return strconv.FormatInt(n, 10)
}

func humanDur(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", ms)
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
