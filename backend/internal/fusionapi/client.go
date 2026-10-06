package fusionapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client reads the three stores. Each field is a store's base URL with no trailing slash ("" = that store is not
// configured, which every call reports as unavailable).
type Client struct {
	Prometheus string
	Loki       string
	Tempo      string
	HTTP       *http.Client
	// MaxBytes bounds one store answer; a bigger one is refused rather than held in memory. Default 16 MiB.
	MaxBytes int64
	// Now is the clock, for tests.
	Now func() time.Time
}

const defaultMaxBytes = 16 << 20

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) maxBytes() int64 {
	if c.MaxBytes > 0 {
		return c.MaxBytes
	}
	return defaultMaxBytes
}

// Store names, for messages.
const (
	storeProm  = "Prometheus"
	storeLoki  = "Loki"
	storeTempo = "Tempo"
)

// get calls one store and decodes a JSON answer into out (a *json.RawMessage keeps it verbatim).
func (c *Client) get(ctx context.Context, store, base, path string, q url.Values, hdr map[string]string, out any) error {
	if base == "" {
		return errf(http.StatusServiceUnavailable, "%s is not configured on this server", store)
	}
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return errf(http.StatusInternalServerError, "could not build the %s request", store)
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errf(http.StatusServiceUnavailable, "%s is not reachable. FUSION is off or still starting", store)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes()+1))
	if err != nil {
		return errf(http.StatusBadGateway, "%s: the answer was cut off", store)
	}
	if int64(len(body)) > c.maxBytes() {
		return errf(http.StatusUnprocessableEntity, "%s: the answer is larger than this API returns (%d MiB). Narrow the time range or the filters", store, c.maxBytes()>>20)
	}
	switch {
	case resp.StatusCode/100 == 2:
		if out == nil {
			return nil
		}
		if raw, ok := out.(*json.RawMessage); ok {
			*raw = body
			return nil
		}
		if err := json.Unmarshal(body, out); err != nil {
			return errf(http.StatusBadGateway, "%s answered with something that is not JSON", store)
		}
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return errf(http.StatusNotFound, "%s: not found", store)
	case resp.StatusCode == http.StatusTooManyRequests:
		return errf(http.StatusTooManyRequests, "%s is busy; try again shortly", store)
	case resp.StatusCode/100 == 4:
		return errf(http.StatusBadRequest, "%s refused the query: %s", store, upstreamMessage(body))
	default:
		return errf(http.StatusBadGateway, "%s answered %d: %s", store, resp.StatusCode, upstreamMessage(body))
	}
}

// upstreamMessage pulls the human part out of a store's error body (Prometheus and Loki answer JSON with "error",
// Tempo plain text) and keeps it short.
func upstreamMessage(body []byte) string {
	var j struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &j) == nil {
		if j.Error != "" {
			msg = j.Error
		} else if j.Message != "" {
			msg = j.Message
		}
	}
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	return msg
}

// IsUnavailable reports whether err is a store that could not be reached (FUSION off or starting), as opposed to a
// bad request or a store error. The fused views treat it as a missing part rather than a failure.
func IsUnavailable(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusServiceUnavailable
}

// TimeRange is a closed interval of wall-clock time.
type TimeRange struct{ From, To time.Time }

// Range limits.
const (
	DefaultWindow = time.Hour
	MaxWindow     = 31 * 24 * time.Hour
)

// ParseRange reads the from/to parameters: each is an RFC 3339 time, unix seconds, or "now" / "now-<duration>"
// (for example now-15m). Missing "to" is now; missing "from" is DefaultWindow before "to".
func ParseRange(from, to string, now time.Time) (TimeRange, error) {
	t2 := now
	if to != "" {
		t, err := parseTime(to, now)
		if err != nil {
			return TimeRange{}, badRequest("to: %v", err)
		}
		t2 = t
	}
	t1 := t2.Add(-DefaultWindow)
	if from != "" {
		t, err := parseTime(from, now)
		if err != nil {
			return TimeRange{}, badRequest("from: %v", err)
		}
		t1 = t
	}
	if !t1.Before(t2) {
		return TimeRange{}, badRequest("from must be before to")
	}
	if t2.Sub(t1) > MaxWindow {
		return TimeRange{}, badRequest("the time range is longer than %d days", int(MaxWindow.Hours()/24))
	}
	return TimeRange{From: t1.UTC(), To: t2.UTC()}, nil
}

func parseTime(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "now":
		return now, nil
	case strings.HasPrefix(s, "now-"):
		d, err := time.ParseDuration(strings.TrimPrefix(s, "now-"))
		if err != nil || d < 0 {
			return time.Time{}, errors.New("not a time (use RFC 3339, unix seconds, or now-<duration> such as now-15m)")
		}
		return now.Add(-d), nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
		return time.Unix(0, int64(f*1e9)), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New("not a time (use RFC 3339, unix seconds, or now-<duration> such as now-15m)")
}

// Limit parses a result-count parameter: def when absent, never above max.
func Limit(s string, def, max int) (int, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, badRequest("limit must be a whole number from 1 to %d", max)
	}
	if n > max {
		n = max
	}
	return n, nil
}

func unixSec(t time.Time) string  { return strconv.FormatInt(t.Unix(), 10) }
func unixNano(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }
func unixFloat(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixNano())/1e9, 'f', 3, 64)
}
