package httpkit

import (
	"net/http/httptest"
	"testing"

	"ikhnos/internal/kernel"
)

func TestQueryLimitSaysSoWhenTheNumberIsNotOne(t *testing.T) {
	for _, c := range []struct {
		q    string
		want int
		bad  bool
	}{
		{"", 0, false}, {"limit=5", 5, false}, {"limit=5000", 5000, false},
		{"limit=abc", 0, true}, {"limit=-5", 0, true}, {"limit=0", 0, true}, {"limit=", 0, false}, {"limit=1.5", 0, true},
	} {
		n, err := QueryLimit(httptest.NewRequest("GET", "/x?"+c.q, nil))
		if c.bad {
			if e, ok := err.(*kernel.Error); !ok || e.Kind != kernel.KindInvalid {
				t.Errorf("%q: %d, %v, want a 400", c.q, n, err)
			}
			continue
		}
		if err != nil || n != c.want {
			t.Errorf("%q: %d, %v, want %d", c.q, n, err, c.want)
		}
	}
}
