package embedder

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RateLimitedError is a provider's refusal for quota: more requests, or more tokens, than its plan allows in a period. It is not a failure of
// the text, so a caller can tell it from one and wait instead of marking the document as failed.
type RateLimitedError struct {
	Status int
	Body   string
	// RetryAfter is what the provider asked for, zero when it did not say.
	RetryAfter time.Duration
	// Daily is true when the quota that ran out is a daily one: waiting a few seconds does not bring it back.
	Daily bool
}

func (e *RateLimitedError) Error() string {
	if e.Daily {
		return fmt.Sprintf("embedding API returned %d (the daily quota of the provider is used up: wait for tomorrow or change the provider): %s", e.Status, e.Body)
	}
	return fmt.Sprintf("embedding API returned %d: %s", e.Status, e.Body)
}

// What the embedder waits on a refusal for quota, one wait per new attempt, and the most it waits in all for one request.
var defaultRateLimitWaits = []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second}

const (
	rateLimitBudget = 3 * time.Minute
	// A provider that asks for longer than this in one go is not waited out here: the caller is told how long, and decides.
	rateLimitLongestWait = 2 * time.Minute
)

var (
	retryDelayField   = regexp.MustCompile(`"retryDelay"\s*:\s*"([0-9.]+)s"`)
	retryInMessage    = regexp.MustCompile(`(?i)retry in ([0-9.]+)\s*s`)
	dailyQuotaMarkers = []string{"per day", "perday", "per-day", "daily"}
)

// rateLimitFrom reads a refusal for quota out of a response, or returns nil when the response is any other refusal. A 429 is always one; a
// 503 is one only when the provider says how long to wait.
func rateLimitFrom(status int, header http.Header, body string) *RateLimitedError {
	after := retryAfterOf(header, body)
	if status != http.StatusTooManyRequests && !(status == http.StatusServiceUnavailable && after > 0) {
		return nil
	}
	lower := strings.ToLower(body)
	daily := false
	for _, marker := range dailyQuotaMarkers {
		if strings.Contains(lower, marker) {
			daily = true
			break
		}
	}
	return &RateLimitedError{Status: status, Body: body, RetryAfter: after, Daily: daily}
}

// retryAfterOf is the wait a provider asked for: the Retry-After header (seconds or a date), or the delay Google writes in the body.
func retryAfterOf(header http.Header, body string) time.Duration {
	if value := strings.TrimSpace(header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds >= 0 {
			return time.Duration(seconds * float64(time.Second))
		}
		if at, err := http.ParseTime(value); err == nil {
			return max(0, time.Until(at))
		}
	}
	for _, pattern := range []*regexp.Regexp{retryDelayField, retryInMessage} {
		if match := pattern.FindStringSubmatch(body); match != nil {
			if seconds, err := strconv.ParseFloat(match[1], 64); err == nil {
				return time.Duration(seconds * float64(time.Second))
			}
		}
	}
	return 0
}

// embedBatch sends one group of texts. A refusal for quota is waited out and the same group is sent again; any other error, a refusal that
// says the quota is a daily one, a wait longer than the embedder will hold, or the end of the budget, is returned as it is.
func (e *Embedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	waits := e.waits
	if waits == nil {
		waits = defaultRateLimitWaits
	}
	var waited time.Duration
	for attempt := 0; ; attempt++ {
		vectors, err := e.embedOnce(ctx, texts)
		var limited *RateLimitedError
		if err == nil || !errors.As(err, &limited) || limited.Daily || attempt >= len(waits) {
			return vectors, err
		}
		wait := waits[attempt]
		if limited.RetryAfter > 0 {
			wait = limited.RetryAfter
		}
		if wait > rateLimitLongestWait || waited+wait > rateLimitBudget {
			return nil, err
		}
		if sleepErr := e.pause(ctx, wait); sleepErr != nil {
			return nil, sleepErr
		}
		waited += wait
	}
}

func (e *Embedder) pause(ctx context.Context, wait time.Duration) error {
	if e.sleep != nil {
		return e.sleep(ctx, wait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
