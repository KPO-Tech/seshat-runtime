package embedder

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// refusingServer answers `refusals` requests with the given refusal, then embeds like a provider that works.
func refusingServer(t *testing.T, refusals int, status int, header map[string]string, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(requests.Add(1))
		if n <= refusals {
			for k, v := range header {
				w.Header().Set(k, v)
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		data := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			data[i] = map[string]any{"embedding": []float32{0.1, 0.2, 0.3}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

// recorded returns an embedder that does not wait but notes how long it was asked to.
func recorded(srv *httptest.Server) (*Embedder, *[]time.Duration) {
	e := New(&Config{BaseURL: srv.URL, Model: "m", Provider: ProviderOpenAI})
	var slept []time.Duration
	e.sleep = func(_ context.Context, wait time.Duration) error {
		slept = append(slept, wait)
		return nil
	}
	return e, &slept
}

func TestARefusalForQuotaIsWaitedOutThenSucceeds(t *testing.T) {
	srv, requests := refusingServer(t, 2, http.StatusTooManyRequests, nil, `{"error":{"code":429,"message":"You exceeded your current quota"}}`)
	e, slept := recorded(srv)
	vectors, err := e.EmbedTexts(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("a quota that comes back must not fail the texts: %v", err)
	}
	if len(vectors) != 2 || requests.Load() != 3 {
		t.Fatalf("got %d vectors in %d requests, want 2 vectors in 3", len(vectors), requests.Load())
	}
	if want := []time.Duration{5 * time.Second, 15 * time.Second}; len(*slept) != 2 || (*slept)[0] != want[0] || (*slept)[1] != want[1] {
		t.Fatalf("waited %v, want %v", *slept, want)
	}
}

func TestARefusalThatNeverEndsIsReturnedAsOneForQuota(t *testing.T) {
	srv, requests := refusingServer(t, 100, http.StatusTooManyRequests, nil, `{"error":"slow down"}`)
	e, slept := recorded(srv)
	_, err := e.EmbedTexts(context.Background(), []string{"a"})
	var limited *RateLimitedError
	if !errors.As(err, &limited) {
		t.Fatalf("want a RateLimitedError, got %v", err)
	}
	if requests.Load() != 4 || len(*slept) != 3 {
		t.Fatalf("%d requests and %d waits, want 4 and 3", requests.Load(), len(*slept))
	}
	if !strings.Contains(err.Error(), "embedding API returned 429") {
		t.Fatalf("the message keeps the status it always had: %q", err.Error())
	}
}

func TestTheWaitTheProviderAsksForIsUsed(t *testing.T) {
	cases := []struct {
		name   string
		header map[string]string
		body   string
		want   time.Duration
	}{
		{"Retry-After in seconds", map[string]string{"Retry-After": "7"}, `{}`, 7 * time.Second},
		{"the delay Google writes in the body", nil, `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"34s"}]}}`, 34 * time.Second},
		{"the delay written in the message", nil, `{"error":{"message":"Please retry in 12.5s."}}`, 12500 * time.Millisecond},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := refusingServer(t, 1, http.StatusTooManyRequests, c.header, c.body)
			e, slept := recorded(srv)
			if _, err := e.EmbedTexts(context.Background(), []string{"a"}); err != nil {
				t.Fatal(err)
			}
			if len(*slept) != 1 || (*slept)[0] != c.want {
				t.Fatalf("waited %v, want %v", *slept, c.want)
			}
		})
	}
}

func TestADailyQuotaIsNotWaitedOut(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"Rate limit exceeded: free-models-per-day."}}`,
		`{"error":{"details":[{"violations":[{"quotaId":"EmbedContentRequestsPerDayPerProjectPerModel-FreeTier"}]}]}}`,
	} {
		srv, requests := refusingServer(t, 100, http.StatusTooManyRequests, nil, body)
		e, slept := recorded(srv)
		_, err := e.EmbedTexts(context.Background(), []string{"a"})
		var limited *RateLimitedError
		if !errors.As(err, &limited) || !limited.Daily {
			t.Fatalf("want a daily RateLimitedError, got %v", err)
		}
		if requests.Load() != 1 || len(*slept) != 0 {
			t.Fatalf("a daily quota was tried %d times and waited %v", requests.Load(), *slept)
		}
		if !strings.Contains(err.Error(), "daily quota") {
			t.Fatalf("the message must name the daily quota: %q", err.Error())
		}
	}
}

func TestAWaitLongerThanTheEmbedderHoldsIsReportedWithItsLength(t *testing.T) {
	srv, requests := refusingServer(t, 100, http.StatusTooManyRequests, map[string]string{"Retry-After": "600"}, `{}`)
	e, slept := recorded(srv)
	_, err := e.EmbedTexts(context.Background(), []string{"a"})
	var limited *RateLimitedError
	if !errors.As(err, &limited) || limited.RetryAfter != 10*time.Minute {
		t.Fatalf("want a RateLimitedError that carries 10 minutes, got %v", err)
	}
	if requests.Load() != 1 || len(*slept) != 0 {
		t.Fatalf("%d requests, waited %v", requests.Load(), *slept)
	}
}

func TestTheTotalWaitStaysInsideTheBudget(t *testing.T) {
	// Three waits of 100 s are each allowed, but not together.
	srv, _ := refusingServer(t, 100, http.StatusTooManyRequests, map[string]string{"Retry-After": "100"}, `{}`)
	e, slept := recorded(srv)
	_, err := e.EmbedTexts(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("expected the refusal to be returned at the end of the budget")
	}
	var total time.Duration
	for _, d := range *slept {
		total += d
	}
	if total > rateLimitBudget {
		t.Fatalf("waited %v, more than the budget of %v", total, rateLimitBudget)
	}
}

func TestA503IsWaitedOutOnlyWhenTheProviderSaysHowLong(t *testing.T) {
	srv, requests := refusingServer(t, 100, http.StatusServiceUnavailable, nil, `overloaded`)
	e, slept := recorded(srv)
	_, err := e.EmbedTexts(context.Background(), []string{"a"})
	var limited *RateLimitedError
	if err == nil || errors.As(err, &limited) || requests.Load() != 1 || len(*slept) != 0 {
		t.Fatalf("a 503 with no delay is an ordinary error: %v after %d requests", err, requests.Load())
	}

	srv, _ = refusingServer(t, 1, http.StatusServiceUnavailable, map[string]string{"Retry-After": "3"}, `overloaded`)
	e, slept = recorded(srv)
	if _, err := e.EmbedTexts(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if len(*slept) != 1 || (*slept)[0] != 3*time.Second {
		t.Fatalf("waited %v, want 3s", *slept)
	}
}

func TestAnotherRefusalIsNotWaitedOut(t *testing.T) {
	srv, requests := refusingServer(t, 100, http.StatusUnauthorized, nil, `{"error":"bad key"}`)
	e, slept := recorded(srv)
	if _, err := e.EmbedTexts(context.Background(), []string{"a"}); err == nil || requests.Load() != 1 || len(*slept) != 0 {
		t.Fatalf("a bad key is returned at once: %v after %d requests", err, requests.Load())
	}
}

func TestTheWaitEndsWithTheContext(t *testing.T) {
	srv, _ := refusingServer(t, 100, http.StatusTooManyRequests, nil, `{}`)
	e := New(&Config{BaseURL: srv.URL, Model: "m", Provider: ProviderOpenAI})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := e.EmbedTexts(ctx, []string{"a"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the context's error, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("the wait did not stop with the context: %v", time.Since(start))
	}
}
