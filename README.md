# ai-status

Ferramentas de monitoramento local para o stack Qwen Code:

| Ferramenta | Caminho | O que faz |
|---|---|---|
| `ai-status` | `.` (raiz) | Dashboard do servidor SGLang (Prometheus `/metrics`) |
| `qwen-status` | [`cmd/qwen-status`](cmd/qwen-status) | Monitor de agentes qwen-code: sessões vivas, tokens, histórico (lê `~/.qwen`, sem rede) |

---

## ai-status (raiz)

Dashboard de terminal para o servidor SGLang usado pelo Qwen Code. Lê o endpoint
Prometheus `/metrics` a cada segundo e mostra throughput, sessões em paralelo,
KV cache, latência e speculative decoding. Só stdlib do Go, zero dependências.

## Uso

```sh
go build -o ai-status .
./ai-status
```

Monitora vários servidores SGLang. A barra no topo mostra os providers; troque
com `1`-`9`, `tab` ou `←`/`→` (`q` sai). A lista vem de `~/.qwen/settings.json`
(providers cujo nome/descrição contém "sglang", o do modelo ativo primeiro) mais
`extraProviders` em `main.go` (hoje `http://10.162.1.23:11436`). O sufixo `/v1`
é removido e URLs repetidas são ignoradas. Ao trocar, as janelas e históricos
recomeçam.

| Flag   | Padrão | Descrição                                    |
|--------|--------|----------------------------------------------|
| `-url` | config | URLs base do SGLang separadas por vírgula, ex. `http://a:11435,http://b:11436` |
| `-i`   | `1s`   | intervalo de polling                         |
| `-w`   | `5`    | janela das taxas e médias, em amostras       |

Sair: `q` ou `ctrl-c`.

## Requisito

O SGLang precisa subir com `--enable-metrics`. Sem isso `/metrics` retorna 404
e o dashboard mostra o erro.

## Painéis

- **Throughput**: tok/s de decode e de prefill (de `realtime_tokens_total`,
  que avança durante a geração), tokens servidos pelo prefix cache, gauge
  `gen_throughput` do scheduler, req/s.
- **Sessions**: requests rodando, na fila (grammar/paused/retracted), HTTP
  ativos, utilização.
- **KV cache**: uso em % e tokens usados/máximo, histórico, tokens evictable do
  radix cache, taxa de acerto do prefix cache, estado mamba (modelos híbridos),
  soma dos contextos em decode.
- **Latency**: TTFT, e2e e tempo de fila, médios na janela (ou média geral se
  nenhum request terminou na janela).
- **Speculative decoding**: accept length e accept rate.
- **Totals**: requests, tokens de prompt/geração, memória GPU (pesos, KV,
  CUDA graph), context length.

Séries com múltiplos labels são somadas; séries com label `mode="x"` também
ficam acessíveis como `nome:x`.

## Testes

```sh
go test ./...
```
