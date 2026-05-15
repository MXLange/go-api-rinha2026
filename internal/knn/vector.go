package knn

import (
	"math"

	"go-api-rinha2026/internal/fastjson"
)

const (
	Dims       = 14
	PackedDims = 16
	Scale      = int16(10000)
	K          = 5
	Lanes      = 8
)

type QueryVector [PackedDims]int16

func FromPayload(p fastjson.Payload) QueryVector {
	var out QueryVector

	out[0] = quantize(p.Amount / 10_000.0)
	out[1] = quantize(float64(p.Installments) / 12.0)
	if p.CustomerAvgAmount > 0 {
		out[2] = quantize((p.Amount / p.CustomerAvgAmount) / 10.0)
	} else {
		out[2] = Scale
	}
	out[3] = quantize(float64(p.Hour) / 23.0)
	out[4] = quantize(float64(p.DayOfWeek) / 6.0)

	if p.HasLastTx {
		out[5] = quantize(float64(p.MinutesSinceLast) / 1440.0)
		out[6] = quantize(p.KmFromCurrent / 1000.0)
	} else {
		out[5] = -Scale
		out[6] = -Scale
	}

	out[7] = quantize(p.KmFromHome / 1000.0)
	out[8] = quantize(float64(p.TxCount24h) / 20.0)
	if p.IsOnline {
		out[9] = Scale
	}
	if p.CardPresent {
		out[10] = Scale
	}
	if p.IsUnknownMerchant {
		out[11] = Scale
	}
	out[12] = quantize(mccRisk(p.MCC))
	out[13] = quantize(p.MerchantAvgAmount / 10_000.0)

	return out
}

func QuantizeReference(values []float64) QueryVector {
	var out QueryVector
	for i := 0; i < Dims && i < len(values); i++ {
		out[i] = quantize(values[i])
	}
	return out
}

func PartitionKey(v *QueryVector) uint32 {
	var key uint32
	if v[5] >= 0 {
		key |= 1 << 0
	}
	if v[9] > 0 {
		key |= 1 << 1
	}
	if v[10] > 0 {
		key |= 1 << 2
	}
	if v[11] > 0 {
		key |= 1 << 3
	}

	switch {
	case v[12] <= 2047:
	case v[12] <= 4095:
		key |= 1 << 4
	case v[12] <= 6143:
		key |= 2 << 4
	default:
		key |= 3 << 4
	}

	if v[2] > 4096 {
		key |= 1 << 6
	}
	if v[8] > 2048 {
		key |= 1 << 7
	}
	return key
}

func quantize(value float64) int16 {
	if value <= -1.0 {
		return -Scale
	}
	if value <= 0.0 {
		return 0
	}
	if value >= 1.0 {
		return Scale
	}
	return int16(math.Round(value * float64(Scale)))
}

func mccRisk(mcc uint32) float64 {
	switch mcc {
	case 5411:
		return 0.15
	case 5812:
		return 0.30
	case 5912:
		return 0.20
	case 5944:
		return 0.45
	case 7801:
		return 0.80
	case 7802:
		return 0.75
	case 7995:
		return 0.85
	case 4511:
		return 0.35
	case 5311:
		return 0.25
	case 5999:
		return 0.50
	default:
		return 0.50
	}
}
