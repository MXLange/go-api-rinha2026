# improve-lb

## Objetivo

Reduzir a latencia adicionada pelo LB sem aumentar CPU/memoria e sem mudar a semantica da API.

O pprof do LB mostrou CPU praticamente nula:

```text
Duration: 150.09s
Total samples: 20ms
CPU ativo: 0.013%
```

Entao o gargalo do LB, se existir, nao e CPU. O risco esta em latencia de syscalls por request.

## Estado atual

Fluxo por conexao recebida:

1. `AcceptTCP`;
2. round-robin da API;
3. `net.DialUnix` para o socket da API;
4. `WriteMsgUnix` com `SCM_RIGHTS`;
5. `Close` da conexao Unix de controle;
6. `Close` da conexao TCP no LB.

Ponto suspeito:

```text
DialUnix + WriteMsgUnix + Close por request
```

Isso quase nao aparece em CPU, mas pode pesar no p99.

## Metricas

Usar o override de observabilidade:

```bash
docker compose -f docker-compose.yml -f docker-compose.observability.yml up
```

Medir:

```bash
curl -s http://127.0.0.1:6063/metrics
```

Foco:

```text
fd_pass.avg_us
fd_pass buckets <=50us, <=100us, <=200us
pass_errors
```

Tambem rodar pprof do LB durante o teste completo:

```bash
go tool pprof -http=127.0.0.1:8083 \
  'http://127.0.0.1:6063/debug/pprof/profile?seconds=60'
```

## Plano de melhoria

### 1. Baseline com histogramas

Rodar o teste atual e salvar:

```text
p99 final
fd_pass avg_us
fd_pass max_us
fd_pass distribuicao por buckets
http_errors
```

Essa etapa evita otimizar o LB se `fd_pass` ja estiver baixo o suficiente.

### 2. Conexao Unix persistente por API

Trocar o modelo atual:

```text
DialUnix por request
```

por:

```text
1 UnixConn persistente por upstream/worker
```

Ideia:

- no startup, o LB abre conexoes Unix para cada API;
- cada worker mantem seu proprio conjunto de conexoes para evitar lock;
- por request, o LB faz apenas `WriteMsgUnix` na conexao persistente;
- em erro, reconecta aquele upstream.

Impacto esperado:

```text
menos syscalls
menos alocacao interna de net.DialUnix
menos jitter no p99
```

Risco:

```text
precisamos garantir que a API aceita multiplos FDs no mesmo UnixConn
```

Hoje a API aceita uma conexao Unix por FD. Para conexao persistente, a API precisa ler em loop no mesmo controle.

### 3. Ajustar API para controle persistente

Alterar `ServeFD`:

- cada `AcceptUnix` vira uma conexao de controle persistente;
- dentro dela, a API faz loop de `recvFD`;
- cada FD recebido vira uma request TCP tratada como hoje.

Isso reduz churn de conexoes Unix entre LB e API.

### 4. Evitar `SetKeepAlive` por conexao no LB

Hoje o LB chama:

```go
conn.SetNoDelay(true)
conn.SetKeepAlive(true)
conn.SetKeepAlivePeriod(30 * time.Second)
```

Testar remover `SetKeepAlive` e `SetKeepAlivePeriod`.

Motivo:

- o k6 usa conexoes curtas/keep-alive controlado pelo cliente;
- essas chamadas podem adicionar syscalls por conexao;
- `SetNoDelay` provavelmente deve ficar.

Validar se p99 melhora e se nao surgem HTTP errors.

### 5. Reavaliar `WORKERS`

Testar:

```text
WORKERS=1
WORKERS=2
WORKERS=3
```

Como o LB quase nao usa CPU, mais workers podem apenas adicionar disputa no `AcceptTCP`.

Metrica principal:

```text
p99 final
fd_pass buckets
http_errors
```

## Criterio de aceite

Aceitar mudanca se:

```text
final_score continua 6000
http_errors = 0
p99 reduz de forma repetivel
fd_pass avg/max reduz
CPU do LB continua abaixo do limite
```

Rejeitar se:

```text
adiciona instabilidade
aumenta http_errors
melhora media mas piora p99
complica a API sem reducao visivel no fd_pass
```
