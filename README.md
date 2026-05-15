# go-api-rinha2026

API 100% Go inspirada no melhor projeto Rust analisado: build-time preprocessing, indice kNN exato em arquivo binario, `mmap` no runtime, parser manual, respostas HTTP fixas e LB Go com Unix socket FD passing.

## Preprocessamento

O build le somente `resources/references.json.gz`, quantiza os 3M vetores em `i16` com `scale=10000`, particiona por features fortes e cria uma arvore por particao:

```bash
go run ./cmd/build-index \
  --input resources/references.json.gz \
  --out data/knn.idx \
  --leaf-size 48
```

O arquivo gerado fica perto de `99M` e e carregado por `mmap` pela API.

## Runtime

Por request:

1. o LB Go aceita TCP em `:9999`;
2. o LB passa o socket aceito para uma API via `SCM_RIGHTS`;
3. a API parseia o JSON manualmente;
4. a transacao vira vetor 14D quantizado;
5. o indice faz kNN exato com `k=5`;
6. `fraud_score = fraudes_entre_5_vizinhos / 5`;
7. a resposta HTTP vem de uma tabela fixa.

Nao usa `tx id`, `test-data.json`, capturas do k6 ou excecoes por caso conhecido no runtime.

## Build

```bash
docker build -t mxlange/go-api-rinha2026:latest .
```

O Dockerfile compila `/app/api`, `/app/lb` e embute `/app/data/knn.idx`.

## Run

```bash
docker compose up
```

Servico em `http://localhost:9999`, com dois containers de API e um container LB, todos usando a mesma imagem Go.

## Verificacao offline

Para validar a semantica contra o test-data local, sem colocar isso no runtime:

```bash
go run ./cmd/verify-index \
  --index data/knn.idx \
  --test-data /home/murillo/apps/rinha-2026/rinha-de-backend-2026/test/test-data.json
```

Resultado local apos a porta:

```text
verified=54100 parse_errors=0 score_mismatch=0 false_positive=0 false_negative=0
```
