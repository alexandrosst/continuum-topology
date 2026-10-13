package link

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseRate reads a bit rate such as "200M", "1G", "512k" or "1500000" (decimal multiples, bits per second). "0" and "" are
// zero.
func ParseRate(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	mult := uint64(1)
	switch last := s[len(s)-1]; last {
	case 'k', 'K':
		mult = 1e3
	case 'm', 'M':
		mult = 1e6
	case 'g', 'G':
		mult = 1e9
	case 't', 'T':
		mult = 1e12
	}
	num := s
	if mult != 1 {
		num = s[:len(s)-1]
	}
	return scale(s, num, mult)
}

// ParseBytes reads a size such as "10Gi" (binary), "500MB" or "2G" (decimal), or a plain number of bytes. "0" and "" are zero.
func ParseBytes(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	t := strings.TrimSuffix(strings.TrimSuffix(s, "B"), "b")
	mult := uint64(1)
	for _, u := range []struct {
		suffix string
		mult   uint64
	}{{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}, {"K", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12}} {
		if strings.HasSuffix(t, u.suffix) {
			mult, t = u.mult, strings.TrimSuffix(t, u.suffix)
			break
		}
	}
	return scale(s, t, mult)
}

func scale(orig, num string, mult uint64) (uint64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) || f*float64(mult) > 1e18 {
		return 0, fmt.Errorf("%q is not a size", orig)
	}
	return uint64(f * float64(mult)), nil
}
