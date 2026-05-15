ARG GO_VERSION=1.26.2

FROM golang:${GO_VERSION}-bookworm AS build
ARG RINHA_LEAF_SIZE=80

ENV CGO_ENABLED=0
WORKDIR /src/go-api-rinha2026
COPY . .
RUN go run ./cmd/build-index \
    --input resources/references.json.gz \
    --out data/knn.idx \
    --leaf-size ${RINHA_LEAF_SIZE}
RUN go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api
RUN go build -trimpath -ldflags="-s -w" -o /out/lb ./cmd/lb

FROM scratch AS runtime
WORKDIR /app
COPY --from=build /out/api /app/api
COPY --from=build /out/lb /app/lb
COPY --from=build /src/go-api-rinha2026/data/knn.idx /app/data/knn.idx

CMD ["/app/api"]
