package main

import (
	"log"
	"os"

	"go-api-rinha2026/internal/fastjson"
	"go-api-rinha2026/internal/knn"
	"go-api-rinha2026/internal/response"
	"go-api-rinha2026/internal/server"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	sockPath := getenv("FD_SOCKET", "")
	if sockPath == "" {
		sockPath = getenv("RINHA_FD_SOCKET", "")
	}
	if sockPath == "" {
		log.Fatal("FD_SOCKET is required")
	}

	indexPath := getenv("INDEX_PATH", "")
	if indexPath == "" {
		indexPath = getenv("RINHA_INDEX_PATH", "")
	}
	if indexPath == "" {
		log.Fatal("INDEX_PATH is required")
	}
	index, err := knn.Open(indexPath)
	if err != nil {
		log.Fatalf("index: %v", err)
	}
	defer index.Close()

	handler := func(body []byte) []byte {
		payload, ok := fastjson.Parse(body)
		if !ok {
			return response.Fallback
		}
		q := knn.FromPayload(payload)
		return response.FraudFor(index.PredictFraudCount(&q), 3)
	}

	log.Printf(
		"ready sock=%s index=%s gomaxprocs=%s gomemlimit=%s",
		sockPath,
		indexPath,
		os.Getenv("GOMAXPROCS"),
		os.Getenv("GOMEMLIMIT"),
	)
	if err := server.ServeFD(sockPath, handler); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func getenv(name string, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}
