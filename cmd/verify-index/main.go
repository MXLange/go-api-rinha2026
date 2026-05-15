package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"time"

	"go-api-rinha2026/internal/fastjson"
	"go-api-rinha2026/internal/knn"
)

type testData struct {
	Entries []testEntry `json:"entries"`
}

type testEntry struct {
	Request            json.RawMessage `json:"request"`
	ExpectedApproved   bool            `json:"expected_approved"`
	ExpectedFraudScore float64         `json:"expected_fraud_score"`
}

func main() {
	indexPath := flag.String("index", "data/knn.idx", "index path")
	testPath := flag.String("test-data", "../rinha-de-backend-2026/test/test-data.json", "test-data path")
	limit := flag.Int("limit", 0, "optional max entries")
	flag.Parse()

	start := time.Now()
	index, err := knn.Open(*indexPath)
	if err != nil {
		fail(err)
	}
	defer index.Close()

	raw, err := os.ReadFile(*testPath)
	if err != nil {
		fail(err)
	}
	var data testData
	if err := json.Unmarshal(raw, &data); err != nil {
		fail(err)
	}
	total := len(data.Entries)
	if *limit > 0 && *limit < total {
		total = *limit
	}

	var parseErrors, scoreMismatch, falsePositive, falseNegative int
	for i := 0; i < total; i++ {
		entry := data.Entries[i]
		payload, ok := fastjson.Parse(entry.Request)
		if !ok {
			parseErrors++
			continue
		}
		query := knn.FromPayload(payload)
		count := int(index.PredictFraudCount(&query))
		expectedCount := int(math.Round(entry.ExpectedFraudScore * 5.0))
		if count != expectedCount {
			scoreMismatch++
		}
		approved := count < 3
		if !approved && entry.ExpectedApproved {
			falsePositive++
		} else if approved && !entry.ExpectedApproved {
			falseNegative++
		}
	}

	fmt.Printf(
		"verified=%d parse_errors=%d score_mismatch=%d false_positive=%d false_negative=%d elapsed=%s\n",
		total,
		parseErrors,
		scoreMismatch,
		falsePositive,
		falseNegative,
		time.Since(start).Round(time.Millisecond),
	)
	if parseErrors != 0 || scoreMismatch != 0 || falsePositive != 0 || falseNegative != 0 {
		os.Exit(1)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "verify-index: %v\n", err)
	os.Exit(1)
}
