// qwen-status: store de dados locais do qwen-code.
//
// Três fontes, todas gravadas pelo próprio agente em ~/.qwen:
//   - sessions/<pid>.json            registro de sessões vivas (pid, cwd, versão)
//   - usage/token-usage-YYYY-MM.jsonl um registro por request de API (tokens, latência)
//   - usage_record.jsonl             um resumo por sessão finalizada (tools, arquivos, projeto)
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Store lê os dados locais do qwen-code.
type Store struct{ Root string }

// DefaultStore aponta para ~/.qwen.
func DefaultStore() Store {
	home, err := os.UserHomeDir()
	if err != nil {
		return Store{Root: ".qwen"}
	}
	return Store{Root: filepath.Join(home, ".qwen")}
}

// ---------- sessões vivas ----------

// LiveSession é uma entrada de ~/.qwen/sessions/<pid>.json.
type LiveSession struct {
	PID         int    `json:"pid"`
	SessionID   string `json:"sessionId"`
	CWD         string `json:"cwd"`
	Name        string `json:"name"`
	StartedAt   int64  `json:"startedAt"` // epoch em ms
	QwenVersion string `json:"qwenVersion"`
	Kind        string `json:"kind"`
}

// Live retorna as sessões cujo processo ainda está vivo, da mais recente.
func (s Store) Live() ([]LiveSession, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "sessions"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []LiveSession
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.Root, "sessions", e.Name()))
		if err != nil {
			continue
		}
		var ls LiveSession
		if err := json.Unmarshal(raw, &ls); err != nil || ls.PID <= 0 {
			continue
		}
		if pidAlive(ls.PID) {
			out = append(out, ls)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out, nil
}

// pidAlive verifica existência de processo com signal 0.
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// ---------- uso de tokens por request ----------

// TokenUsage é um request de API, como gravado em usage/token-usage-YYYY-MM.jsonl.
type TokenUsage struct {
	Timestamp      time.Time
	SessionID      string
	Model          string
	AuthType       string
	Source         string // "main" ou nome do subagente/ferramenta
	InputTokens    int64
	OutputTokens   int64
	CachedTokens   int64
	ThoughtsTokens int64
	TotalTokens    int64
	APIDurationMs  int64
}

type tokenUsageWire struct {
	Timestamp      string `json:"timestamp"`
	SessionID      string `json:"sessionId"`
	Model          string `json:"model"`
	AuthType       string `json:"authType"`
	Source         string `json:"source"`
	InputTokens    int64  `json:"inputTokens"`
	OutputTokens   int64  `json:"outputTokens"`
	CachedTokens   int64  `json:"cachedTokens"`
	ThoughtsTokens int64  `json:"thoughtsTokens"`
	TotalTokens    int64  `json:"totalTokens"`
	APIDurationMs  int64  `json:"apiDurationMs"`
}

// Usage carrega os registros desde `since` (inclusive), ordenados por tempo.
func (s Store) Usage(since time.Time) ([]TokenUsage, error) {
	var out []TokenUsage
	for _, f := range s.usageFiles(since) {
		rows, err := readUsageFile(f)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if !r.Timestamp.Before(since) {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	return out, nil
}

// usageFiles lista os arquivos mensais que podem conter linhas desde `since`.
func (s Store) usageFiles(since time.Time) []string {
	var files []string
	month := time.Date(since.Year(), since.Month(), 1, 0, 0, 0, 0, since.Location())
	nowMonth := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.Local)
	for !month.After(nowMonth) {
		p := filepath.Join(s.Root, "usage", "token-usage-"+month.Format("2006-01")+".jsonl")
		if _, err := os.Stat(p); err == nil {
			files = append(files, p)
		}
		month = month.AddDate(0, 1, 0)
	}
	return files
}

func readUsageFile(path string) ([]TokenUsage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []TokenUsage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var w tokenUsageWire
		if err := json.Unmarshal([]byte(line), &w); err != nil {
			continue // linha corrompida: pula
		}
		ts, err := time.Parse(time.RFC3339Nano, w.Timestamp)
		if err != nil {
			continue
		}
		out = append(out, TokenUsage{
			Timestamp:      ts,
			SessionID:      w.SessionID,
			Model:          w.Model,
			AuthType:       w.AuthType,
			Source:         w.Source,
			InputTokens:    w.InputTokens,
			OutputTokens:   w.OutputTokens,
			CachedTokens:   w.CachedTokens,
			ThoughtsTokens: w.ThoughtsTokens,
			TotalTokens:    w.TotalTokens,
			APIDurationMs:  w.APIDurationMs,
		})
	}
	return out, sc.Err()
}

// ---------- resumo por sessão finalizada ----------

// ModelStats é o uso por modelo dentro de uma sessão.
type ModelStats struct {
	Requests       int   `json:"requests"`
	InputTokens    int64 `json:"inputTokens"`
	OutputTokens   int64 `json:"outputTokens"`
	CachedTokens   int64 `json:"cachedTokens"`
	ThoughtsTokens int64 `json:"thoughtsTokens"`
	TotalTokens    int64 `json:"totalTokens"`
	TotalLatencyMs int64 `json:"totalLatencyMs"`
}

// FileStats é o diff de linhas da sessão.
type FileStats struct {
	LinesAdded   int64 `json:"linesAdded"`
	LinesRemoved int64 `json:"linesRemoved"`
}

// SessionRecord é uma linha de ~/.qwen/usage_record.jsonl.
type SessionRecord struct {
	SessionID      string                `json:"sessionId"`
	StartTime      time.Time             `json:"startTime"`
	EndTime        time.Time             `json:"endTime"`
	Project        string                `json:"project"`
	DurationMs     int64                 `json:"durationMs"`
	TotalLatencyMs int64                 `json:"totalLatencyMs"`
	Models         map[string]ModelStats `json:"models"`
	TotalTokens    int64                 `json:"totalTokens"`
	TotalRequests  int                   `json:"totalRequests"`
	ToolCalls      int                   `json:"toolCalls"`
	ToolFails      int                   `json:"toolFails"`
	LinesAdded     int64                 `json:"linesAdded"`
	LinesRemoved   int64                 `json:"linesRemoved"`
}

type sessionRecordWire struct {
	SessionID      string                `json:"sessionId"`
	Timestamp      int64                 `json:"timestamp"`
	StartTime      int64                 `json:"startTime"`
	Project        string                `json:"project"`
	DurationMs     int64                 `json:"durationMs"`
	TotalLatencyMs int64                 `json:"totalLatencyMs"`
	Models         map[string]ModelStats `json:"models"`
	Tools          struct {
		TotalCalls int `json:"totalCalls"`
		TotalFail  int `json:"totalFail"`
	} `json:"tools"`
	Files FileStats `json:"files"`
}

// Records carrega todos os resumos de sessão, do mais recente ao mais antigo.
func (s Store) Records() ([]SessionRecord, error) {
	f, err := os.Open(filepath.Join(s.Root, "usage_record.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []SessionRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var w sessionRecordWire
		if err := json.Unmarshal([]byte(line), &w); err != nil {
			continue
		}
		var tokens int64
		var reqs int
		for _, m := range w.Models {
			tokens += m.TotalTokens
			reqs += m.Requests
		}
		out = append(out, SessionRecord{
			SessionID:      w.SessionID,
			StartTime:      time.UnixMilli(w.StartTime),
			EndTime:        time.UnixMilli(w.Timestamp),
			Project:        w.Project,
			DurationMs:     w.DurationMs,
			TotalLatencyMs: w.TotalLatencyMs,
			Models:         w.Models,
			TotalTokens:    tokens,
			TotalRequests:  reqs,
			ToolCalls:      w.Tools.TotalCalls,
			ToolFails:      w.Tools.TotalFail,
			LinesAdded:     w.Files.LinesAdded,
			LinesRemoved:   w.Files.LinesRemoved,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartTime.After(out[j].StartTime) })
	return out, nil
}

// ProjectBySession mapeia session ID → caminho do projeto, a partir de todos os resumos.
func (s Store) ProjectBySession() (map[string]string, error) {
	recs, err := s.Records()
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(recs))
	for _, r := range recs {
		m[r.SessionID] = r.Project
	}
	return m, nil
}
