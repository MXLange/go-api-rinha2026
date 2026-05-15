package fastjson

import "math"

type Payload struct {
	Amount            float64
	CustomerAvgAmount float64
	MerchantAvgAmount float64
	KmFromHome        float64
	KmFromCurrent     float64
	TxCount24h        uint32
	MCC               uint32
	MinutesSinceLast  uint32
	Installments      uint8
	Hour              uint8
	DayOfWeek         uint8
	IsOnline          bool
	CardPresent       bool
	IsUnknownMerchant bool
	HasLastTx         bool
}

var fracPowers = [...]float64{
	1e0, 1e-1, 1e-2, 1e-3, 1e-4, 1e-5, 1e-6, 1e-7, 1e-8, 1e-9,
	1e-10, 1e-11, 1e-12, 1e-13, 1e-14, 1e-15, 1e-16, 1e-17, 1e-18,
}

func Parse(buf []byte) (Payload, bool) {
	var p Payload
	pos := 0

	if !nextValue(&pos, buf) {
		return p, false
	}
	if !skipString(&pos, buf) {
		return p, false
	}

	if !nextValue(&pos, buf) || !nextValue(&pos, buf) {
		return p, false
	}
	p.Amount = scanFloat64(&pos, buf)

	if !nextValue(&pos, buf) {
		return p, false
	}
	p.Installments = uint8(scanUint32(&pos, buf))

	if !nextValue(&pos, buf) {
		return p, false
	}
	reqY, reqMo, reqD, reqH, reqMin, ok := scanISO(&pos, buf)
	if !ok {
		return p, false
	}
	p.Hour = reqH
	p.DayOfWeek = dayOfWeek(reqY, reqMo, reqD)

	if !nextValue(&pos, buf) || !nextValue(&pos, buf) {
		return p, false
	}
	p.CustomerAvgAmount = scanFloat64(&pos, buf)

	if !nextValue(&pos, buf) {
		return p, false
	}
	p.TxCount24h = scanUint32(&pos, buf)

	if !nextValue(&pos, buf) || pos >= len(buf) {
		return p, false
	}
	pos++ // '['
	var merchants [16][]byte
	merchantCount := 0
	for pos < len(buf) && buf[pos] != ']' {
		if buf[pos] == '"' {
			pos++
			start := pos
			for pos < len(buf) && buf[pos] != '"' {
				pos++
			}
			if pos >= len(buf) {
				return p, false
			}
			if merchantCount < len(merchants) {
				merchants[merchantCount] = buf[start:pos]
				merchantCount++
			}
			pos++
			continue
		}
		pos++
	}
	if pos < len(buf) {
		pos++
	}

	if !nextValue(&pos, buf) || !nextValue(&pos, buf) {
		return p, false
	}
	merchantID, ok := scanString(&pos, buf)
	if !ok {
		return p, false
	}

	if !nextValue(&pos, buf) {
		return p, false
	}
	p.MCC = scanMCC(&pos, buf)

	if !nextValue(&pos, buf) {
		return p, false
	}
	p.MerchantAvgAmount = scanFloat64(&pos, buf)

	if !nextValue(&pos, buf) || !nextValue(&pos, buf) {
		return p, false
	}
	p.IsOnline = scanBool(&pos, buf)

	if !nextValue(&pos, buf) {
		return p, false
	}
	p.CardPresent = scanBool(&pos, buf)

	if !nextValue(&pos, buf) {
		return p, false
	}
	p.KmFromHome = scanFloat64(&pos, buf)

	if !nextValue(&pos, buf) || pos >= len(buf) {
		return p, false
	}
	p.HasLastTx = buf[pos] != 'n'
	if p.HasLastTx {
		if !nextValue(&pos, buf) {
			return p, false
		}
		lastY, lastMo, lastD, lastH, lastMin, ok := scanISO(&pos, buf)
		if !ok {
			return p, false
		}
		if !nextValue(&pos, buf) {
			return p, false
		}
		p.KmFromCurrent = scanFloat64(&pos, buf)
		p.MinutesSinceLast = minutesBetween(lastY, lastMo, lastD, lastH, lastMin, reqY, reqMo, reqD, reqH, reqMin)
	}

	p.IsUnknownMerchant = true
	for i := 0; i < merchantCount; i++ {
		if bytesEqual(merchants[i], merchantID) {
			p.IsUnknownMerchant = false
			break
		}
	}

	return p, true
}

func nextValue(pos *int, buf []byte) bool {
	for *pos < len(buf) {
		switch buf[*pos] {
		case ':':
			(*pos)++
			for *pos < len(buf) {
				switch buf[*pos] {
				case ' ', '\t', '\n', '\r':
					(*pos)++
				default:
					return true
				}
			}
			return false
		case '"':
			(*pos)++
			for *pos < len(buf) && buf[*pos] != '"' {
				(*pos)++
			}
			if *pos >= len(buf) {
				return false
			}
			(*pos)++
		default:
			(*pos)++
		}
	}
	return false
}

func skipString(pos *int, buf []byte) bool {
	if *pos < len(buf) && buf[*pos] == '"' {
		(*pos)++
	}
	for *pos < len(buf) && buf[*pos] != '"' {
		(*pos)++
	}
	if *pos >= len(buf) {
		return false
	}
	(*pos)++
	return true
}

func scanFloat64(pos *int, buf []byte) float64 {
	value, n := parseFloat64(buf[*pos:])
	*pos += n
	return value
}

func parseFloat64(buf []byte) (float64, int) {
	pos := 0
	negative := false
	if pos < len(buf) && buf[pos] == '-' {
		negative = true
		pos++
	}

	var intPart uint64
	for pos < len(buf) && isDigit(buf[pos]) {
		intPart = intPart*10 + uint64(buf[pos]-'0')
		pos++
	}

	value := float64(intPart)
	if pos < len(buf) && buf[pos] == '.' {
		pos++
		start := pos
		var frac uint64
		for pos < len(buf) && isDigit(buf[pos]) {
			if pos-start < 18 {
				frac = frac*10 + uint64(buf[pos]-'0')
			}
			pos++
		}
		digits := pos - start
		if digits > 18 {
			digits = 18
		}
		value += float64(frac) * fracPowers[digits]
	}

	if pos < len(buf) && (buf[pos] == 'e' || buf[pos] == 'E') {
		pos++
		sign := 1
		if pos < len(buf) && (buf[pos] == '+' || buf[pos] == '-') {
			if buf[pos] == '-' {
				sign = -1
			}
			pos++
		}
		exp := 0
		for pos < len(buf) && isDigit(buf[pos]) {
			exp = exp*10 + int(buf[pos]-'0')
			pos++
		}
		value *= math.Pow10(sign * exp)
	}

	if negative {
		value = -value
	}
	return value, pos
}

func scanUint32(pos *int, buf []byte) uint32 {
	var value uint32
	for *pos < len(buf) && isDigit(buf[*pos]) {
		value = value*10 + uint32(buf[*pos]-'0')
		(*pos)++
	}
	return value
}

func scanBool(pos *int, buf []byte) bool {
	isTrue := *pos < len(buf) && buf[*pos] == 't'
	if isTrue {
		*pos += 4
	} else {
		*pos += 5
	}
	return isTrue
}

func scanString(pos *int, buf []byte) ([]byte, bool) {
	if *pos >= len(buf) || buf[*pos] != '"' {
		return nil, false
	}
	(*pos)++
	start := *pos
	for *pos < len(buf) && buf[*pos] != '"' {
		(*pos)++
	}
	if *pos >= len(buf) {
		return nil, false
	}
	value := buf[start:*pos]
	(*pos)++
	return value, true
}

func scanMCC(pos *int, buf []byte) uint32 {
	if *pos < len(buf) && buf[*pos] == '"' {
		(*pos)++
	}
	value := scanUint32(pos, buf)
	if *pos < len(buf) && buf[*pos] == '"' {
		(*pos)++
	}
	return value
}

func scanISO(pos *int, buf []byte) (uint16, uint8, uint8, uint8, uint8, bool) {
	if *pos < len(buf) && buf[*pos] == '"' {
		(*pos)++
	}
	if len(buf)-*pos < 20 {
		return 0, 0, 0, 0, 0, false
	}
	s := buf[*pos:]
	year := uint16(s[0]-'0')*1000 + uint16(s[1]-'0')*100 + uint16(s[2]-'0')*10 + uint16(s[3]-'0')
	month := (s[5]-'0')*10 + (s[6] - '0')
	day := (s[8]-'0')*10 + (s[9] - '0')
	hour := (s[11]-'0')*10 + (s[12] - '0')
	minute := (s[14]-'0')*10 + (s[15] - '0')
	*pos += 20
	for *pos < len(buf) && buf[*pos] != '"' {
		(*pos)++
	}
	if *pos < len(buf) {
		(*pos)++
	}
	return year, month, day, hour, minute, true
}

func dayOfWeek(year uint16, month uint8, day uint8) uint8 {
	t := [...]uint16{0, 3, 2, 5, 0, 3, 5, 1, 4, 6, 2, 4}
	y := uint32(year)
	if month < 3 {
		y--
	}
	dow := (y + y/4 - y/100 + y/400 + uint32(t[month-1]) + uint32(day)) % 7
	return uint8((dow + 6) % 7)
}

func daysSinceEpoch(year int32, month uint32, day uint32) int64 {
	y := year
	if month <= 2 {
		y--
	}
	era := y / 400
	if y < 0 && y%400 != 0 {
		era--
	}
	yoe := uint32(y - era*400)
	m := month
	if m > 2 {
		m -= 3
	} else {
		m += 9
	}
	doy := (153*m+2)/5 + day - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return int64(era)*146097 + int64(doe) - 719468
}

func minutesBetween(y1 uint16, mo1 uint8, d1 uint8, h1 uint8, mi1 uint8, y2 uint16, mo2 uint8, d2 uint8, h2 uint8, mi2 uint8) uint32 {
	day1 := daysSinceEpoch(int32(y1), uint32(mo1), uint32(d1))
	day2 := daysSinceEpoch(int32(y2), uint32(mo2), uint32(d2))
	min1 := day1*1440 + int64(h1)*60 + int64(mi1)
	min2 := day2*1440 + int64(h2)*60 + int64(mi2)
	if min2 <= min1 {
		return 0
	}
	return uint32(min2 - min1)
}

func bytesEqual(a []byte, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}
