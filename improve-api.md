# improve-api

## Objetivo

Reduzir o p99 local mantendo:

```text
false_positive = 0
false_negative = 0
http_errors = 0
final_score = 6000
```

Meta desejada:

```text
p99 atual local: ~0.71ms
meta agressiva: perto de 0.3ms
```

## Leitura do pprof

Profile capturado durante o teste completo:

```text
Duration: 150.03s
Total samples: 3.53s
CPU ativo na API: 2.35%
```

Top CPU:

```text
Syscall6                         1.21s  34.28%
knn.distanceLane                 1.07s  30.31%
knn.lowerBound                   0.58s  16.43%
knn.scanLeaf                     1.26s  35.69% acumulado
knn.searchNode                   1.71s  48.44% acumulado
knn.PredictFraudCount            1.95s  55.24% acumulado
server.writeAll                  0.88s  24.93% acumulado
fastjson.Parse                   0.04s   1.13% acumulado
obs.Histogram.Observe            0.02s   0.57% acumulado
```

Conclusao:

```text
Parser nao e gargalo.
Observabilidade nao domina CPU, mas deve ficar desligada em benchmark oficial.
Gargalos principais: kNN scalar e I/O de resposta.
```

## Metricas

Rodar sempre tres validacoes:

```bash
go test ./...

go run ./cmd/build-index \
  --input resources/references.json.gz \
  --out /tmp/go-api-rinha2026-knn.idx \
  --leaf-size 48

go run ./cmd/verify-index \
  --index /tmp/go-api-rinha2026-knn.idx \
  --test-data /home/murillo/apps/rinha-2026/rinha-de-backend-2026/test/test-data.json
```

Durante benchmark local com observabilidade:

```text
API metrics: json_parse, vector, knn, handler_total
LB metrics: fd_pass
pprof API
pprof LB
```

## Plano de melhoria

### 1. Calcular as 8 lanes do bloco em uma passada

Estado atual:

```text
scanLeaf
  para cada bloco
    para cada lane
      distanceLane(vectors, base, lane, query)
```

Problema:

```text
distanceLane custa 30.31% da CPU.
O loop de 14 dimensoes roda separadamente para cada lane.
```

Como o layout do indice e AoSoA:

```text
dim0 lane0..7
dim1 lane0..7
...
dim13 lane0..7
```

Podemos calcular oito distancias no mesmo loop:

```text
d0..d7 = 0
para cada dimensao:
  carregar q
  calcular diff de lane0..lane7
  acumular diff*diff em d0..d7
inserir os lane_count resultados no top-k
```

Beneficio esperado:

```text
menos overhead de chamada
menos loop externo
melhor localidade de leitura
mais chance do compilador otimizar
```

Risco:

```text
codigo maior e menos generico
precisa respeitar lane_count no ultimo bloco da folha
```

### 2. Unroll manual de `lowerBound`

`lowerBound` custa:

```text
0.58s flat
16.43% da CPU
```

Hoje:

```go
for d := 0; d < Dims; d++ {
    ...
}
```

Como `Dims=14` e fixo, criar versao unrolled:

```text
boundDim(0) + boundDim(1) + ... + boundDim(13)
```

Ou gerar helper inline por dimensao.

Beneficio esperado:

```text
menos branch/loop overhead
menos bounds-check em slice/array
```

Risco:

```text
codigo mais verboso
ganho precisa ser medido
```

### 3. Sweep de `leaf-size` especifico para Go

Rust usa `leaf_size=48`, mas Go scalar pode preferir outro ponto.

Testar:

```text
32, 40, 48, 64, 80, 96, 128, 160
```

Hipotese:

```text
folhas maiores reduzem custo de arvore/lowerBound
folhas menores reduzem distanceLane
```

Como o profile mostra:

```text
distanceLane > lowerBound
```

Nao e obvio qual lado ganha. Precisa medir.

Para cada leaf:

```bash
go run ./cmd/build-index \
  --input resources/references.json.gz \
  --out /tmp/go-api-rinha2026-leaf-LEAF.idx \
  --leaf-size LEAF
```

Depois rodar stack local e benchmark.

Registrar:

```text
leaf_size
index_size
p99
knn.avg_us
knn buckets
final_score
http_errors
```

### 4. Otimizacao maior de I/O

A API hoje transforma o FD recebido em `net.Conn`:

```text
FD recebido via SCM_RIGHTS
os.NewFile
net.FileConn
serveConn(net.Conn)
conn.Read
conn.Write
```

O profile mostra custo relevante em I/O:

```text
server.writeAll                  0.88s  24.93%
net.(*conn).Write                0.88s  24.93%
syscall.Write                    0.87s  24.65%
net.(*conn).Read                 0.21s   5.95%
```

Proposta:

```text
trocar o hot path para operar direto no FD com syscall.Read/syscall.Write
```

Novo fluxo:

```text
recvFD retorna int fd
serveFD(fd, handler)
syscall.Read(fd, rx)
syscall.Write(fd, response)
syscall.Close(fd)
```

Beneficios esperados:

```text
evita net.FileConn
evita netpoll/net.Conn no hot path
reduz overhead de interface net.Conn
reduz custo de Close via netFD
```

Cuidados:

```text
tratar EAGAIN/EINTR corretamente
garantir fechamento do fd
manter TCP_NODELAY, se necessario, via syscall.SetsockoptInt
verificar se fd vem em modo blocking ou non-blocking
```

Implementacao sugerida:

1. adicionar `ServeRawFD(sockPath, handler)`;
2. manter `ServeFD` antigo temporariamente para comparacao;
3. controlar por env:

```text
RAW_FD=1
```

4. comparar p99, http_errors e pprof;
5. se ganhar, remover caminho antigo.

### 5. Separar pprof de histogramas por request

Hoje `OBS_ADDR` liga:

```text
/metrics com time.Now por request
/debug/pprof
```

Para medir CPU com menos interferencia:

```text
PPROF_ADDR=:6060
OBS_TIMINGS=0
```

Modelo sugerido:

```text
PPROF_ADDR habilita apenas pprof
OBS_ADDR + OBS_TIMINGS=1 habilita histogramas por request
```

Beneficio:

```text
profile de CPU mais limpo
benchmark com pprof sem custo de atomics/time.Now no handler
```

### 6. Preparar caminho para SIMD/assembly somente se necessario

Se os passos pure Go nao chegarem perto da meta:

```text
implementar distance block AVX2 em assembly Go
```

Isso foge do caminho mais simples, mas mira diretamente o maior custo:

```text
distanceLane 30.31%
```

Manter fallback pure Go para compatibilidade.

## Ordem recomendada

1. 8-lanes por bloco em pure Go.
2. Unroll de `lowerBound`.
3. Sweep de `leaf-size`.
4. Raw FD read/write.
5. Separar pprof de histogramas.
6. AVX2/assembly se ainda fizer sentido.

## Criterio de aceite

Aceitar cada mudanca se:

```text
verify-index: score_mismatch=0 FP=0 FN=0
benchmark: final_score=6000
http_errors=0
p99 reduz de forma repetivel
CPU/memoria seguem dentro do limite
```

Rejeitar se:

```text
melhora microbenchmark mas piora p99
aumenta http_errors
introduz instabilidade de socket
complica muito sem ganho mensuravel
```
