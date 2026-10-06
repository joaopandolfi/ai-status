// qwen-status: monitor de agentes qwen-code — sessões vivas, uso de tokens e histórico.
//
// Lê apenas os dados locais que o próprio agente grava em ~/.qwen (nenhuma chamada
// de rede). Uso:
//
//	qwen-status              overview (agentes vivos + uso de hoje)
//	qwen-status live         agentes rodando agora
//	qwen-status usage        agregado de tokens (-days, -model, -project)
//	qwen-status sessions     sessões finalizadas recentes (-limit)
//	qwen-status watch        dashboard com polling (-i)
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// visible devolve a largura em colunas de uma linha, ignorando escapes ANSI.
func visible(s string) int {
	return utf8.RuneCountInString(ansiRe.ReplaceAllString(s, ""))
}

const usageText = `qwen-status — monitor de agentes qwen-code (lê ~/.qwen, sem rede)

Uso:
  qwen-status [status]        overview: agentes vivos + uso de hoje
  qwen-status live            agentes rodando agora, com tokens da sessão
  qwen-status usage           uso de tokens agregado
  qwen-status sessions        histórico de sessões finalizadas
  qwen-status watch           dashboard com polling a cada -i
  qwen-status help            esta ajuda

Flags:
  -home DIR     diretório de dados do qwen (padrão ~/.qwen)
  -days N       período em dias (usage, padrão 7)
  -model M      filtra por modelo (usage)
  -project P    filtra por projeto, path completo ou basename (usage)
  -limit N      número de sessões (sessions, padrão 10)
  -i D          intervalo do watch (padrão 2s)
  -json         saída JSON (usage, sessions)
`

func main() {
	cmd, args := splitCommand(os.Args[1:])
	run(cmd, args)
}

// splitCommand separa o comando dos argumentos; sem args vale o padrão "status".
func splitCommand(args []string) (string, []string) {
	if len(args) == 0 {
		return "status", nil
	}
	return args[0], args[1:]
}

func run(cmd string, args []string) {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	home := fs.String("home", "", "diretório de dados do qwen (padrão ~/.qwen)")
	days := fs.Int("days", 7, "período em dias (usage)")
	model := fs.String("model", "", "filtra por modelo (usage)")
	project := fs.String("project", "", "filtra por projeto (usage)")
	limit := fs.Int("limit", 10, "número de sessões (sessions)")
	interval := fs.Duration("i", 2*time.Second, "intervalo do watch")
	jsonOut := fs.Bool("json", false, "saída JSON (usage, sessions)")
	_ = fs.Parse(args)

	d := DefaultStore()
	if *home != "" {
		d.Root = *home
	}

	var err error
	switch cmd {
	case "status":
		err = Overview(os.Stdout, d, time.Now())
	case "live":
		err = LiveReport(os.Stdout, d, time.Now())
	case "usage":
		var agg *UsageAgg
		var w io.Writer = os.Stdout
		if *jsonOut {
			w = io.Discard
		}
		agg, err = UsageReport(w, d, time.Now(), *days, *model, *project)
		if err == nil && *jsonOut {
			err = printJSON(agg)
		}
	case "sessions":
		var recs []SessionRecord
		var w io.Writer = os.Stdout
		if *jsonOut {
			w = io.Discard
		}
		recs, err = SessionsReport(w, d, *limit)
		if err == nil && *jsonOut {
			err = printJSON(recs)
		}
	case "watch":
		err = watch(d, *interval)
	case "help", "-h", "--help":
		fmt.Print(usageText)
	default:
		fmt.Fprintln(os.Stderr, "comando desconhecido:", cmd)
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		os.Exit(1)
	}
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func watch(d Store, interval time.Duration) error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	fmt.Print("\033[2J\033[H\033[?25l") // limpa 1x, esconde o cursor (evita piscar em cima do texto)
	defer fmt.Print("\033[?25h")       // devolve o cursor ao sair
	prev := []string{}
	for {
		var b bytes.Buffer
		if err := Overview(&b, d, time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, "erro:", err)
		}
		b.WriteString(paint(fmt.Sprintf("atualiza a cada %s · ctrl-c sai", interval), dim))
		lines := strings.Split(b.String(), "\n")

		// Sobrescreve no lugar: volta ao topo, pinta o frame novo e estica cada
		// linha com espaços até a largura da anterior — nada de 2J por tick.
		var out strings.Builder
		out.WriteString("\033[H")
		for i, line := range lines {
			if i > 0 {
				out.WriteString("\r\n")
			}
			out.WriteString(line)
			if i < len(prev) && visible(prev[i]) > visible(line) {
				out.WriteString(strings.Repeat(" ", visible(prev[i])-visible(line)))
			}
		}
		for i := len(lines); i < len(prev); i++ {
			out.WriteString("\r\n")
			out.WriteString(strings.Repeat(" ", visible(prev[i])))
		}
		if _, err := os.Stdout.WriteString(out.String()); err != nil {
			return err
		}
		prev = lines

		select {
		case <-sig:
			return nil
		case <-time.After(interval):
		}
	}
}
