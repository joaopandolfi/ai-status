# qwen-status

Monitor de agentes **qwen-code**: sessões vivas, uso de tokens e histórico de
sessões. Lê apenas os dados locais que o próprio agente grava em `~/.qwen` —
**nenhuma chamada de rede**, não interfere no agente.

Só stdlib do Go, zero dependências.

## Fontes de dados

| Arquivo | Conteúdo |
|---|---|
| `~/.qwen/sessions/<pid>.json` | registro de sessões (pid, projeto, versão, início) — o filtro por processo vivo é feito com signal 0 |
| `~/.qwen/usage/token-usage-YYYY-MM.jsonl` | um registro por request de API: sessão, modelo, `source` (main/subagente), tokens (input/output/cached/thoughts), latência |
| `~/.qwen/usage_record.jsonl` | um resumo por sessão finalizada: projeto, duração, models, tools (com falhas), diff de linhas |

## Build e uso

```sh
go build -o qwen-status ./cmd/qwen-status

qwen-status              # overview: agentes vivos + uso de hoje
qwen-status live         # agentes rodando agora, com tokens/reqs/modelo da sessão
qwen-status usage        # agregado de tokens (por dia, por modelo, main vs sub)
qwen-status sessions     # sessões finalizadas recentes
qwen-status watch        # dashboard com polling (padrão 2s), ctrl-c sai
```

### Flags

| Flag | Padrão | Descrição |
|---|---|---|
| `-home DIR` | `~/.qwen` | diretório de dados do qwen (útil p/ testes) |
| `-days N` | `7` | período em dias (`usage`) |
| `-model M` | — | filtra por modelo (`usage`) |
| `-project P` | — | filtra por projeto: path completo ou basename (`usage`) |
| `-limit N` | `10` | número de sessões (`sessions`) |
| `-i D` | `2s` | intervalo do `watch` |
| `-json` | — | saída JSON (`usage`, `sessions`) — sai puro, sem o relatório humano |

### Exemplo: JSON pra pipeline

```sh
qwen-status usage -days 1 -json | jq '.byDay, .byModel'
qwen-status sessions -limit 5 -json
```

## Testes

```sh
go test ./cmd/qwen-status
```

Os testes usam diretórios temporários com fixtures no formato real dos
arquivos (linhas inválidas são puladas, pids mortos são filtrados).

## Observações

- "main" vs "sub": a coluna `source` do registro distingue o turno principal do
  agente dos subagentes/ferramentas internas (Explore, memory extractor, etc.).
- Tokens de sessões vivas são contabilizados desde o início do mês corrente
  (janela do arquivo mensal); sessão mais velha que isso conta menos.
- Custos em R$/US$ ficam de fora de propósito: os preços dos modelos internos
  variam. O dado bruto (tokens por modelo) é o que importa pra calcular.
