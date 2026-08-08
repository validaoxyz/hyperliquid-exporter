package hyperliquidapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type trackedBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *trackedBody) Close() error {
	b.closed.Store(true)
	return nil
}

func TestNewResolverNormalizesAndRejectsChain(t *testing.T) {
	mainnet, err := NewResolver(" MainNet ")
	if err != nil {
		t.Fatalf("NewResolver(mainnet) error = %v", err)
	}
	if mainnet.GetChain() != "mainnet" || mainnet.GetBaseURL() != "https://api.hyperliquid.xyz" {
		t.Fatalf("unexpected mainnet resolver: chain=%q url=%q", mainnet.GetChain(), mainnet.GetBaseURL())
	}

	testnet, err := NewResolver("TESTNET")
	if err != nil {
		t.Fatalf("NewResolver(testnet) error = %v", err)
	}
	if testnet.GetChain() != "testnet" || testnet.GetBaseURL() != "https://api.hyperliquid-testnet.xyz" {
		t.Fatalf("unexpected testnet resolver: chain=%q url=%q", testnet.GetChain(), testnet.GetBaseURL())
	}

	for _, raw := range []string{"", "devnet", "main-net"} {
		if _, err := NewResolver(raw); err == nil {
			t.Fatalf("NewResolver(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestMakeAPICallRebuildsBodyAndClosesEachResponse(t *testing.T) {
	resolver, err := NewResolver("testnet")
	if err != nil {
		t.Fatal(err)
	}
	resolver.wait = func(context.Context, time.Duration) error { return nil }

	var (
		mu     sync.Mutex
		bodies []string
		closes []*trackedBody
	)
	resolver.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			t.Fatalf("read request body: %v", readErr)
		}
		mu.Lock()
		bodies = append(bodies, string(body))
		attempt := len(bodies)
		responseBody := &trackedBody{Reader: strings.NewReader("retry")}
		status := http.StatusInternalServerError
		if attempt == 3 {
			status = http.StatusOK
			responseBody = &trackedBody{Reader: strings.NewReader("[]")}
		}
		closes = append(closes, responseBody)
		mu.Unlock()
		return &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Body:       responseBody,
			Header:     make(http.Header),
		}, nil
	})}

	var response []ValidatorSummary
	if err := resolver.makeAPICall(context.Background(), "/info", APIRequest{Type: "validatorSummaries"}, &response); err != nil {
		t.Fatalf("makeAPICall() error = %v", err)
	}
	if len(bodies) != 3 {
		t.Fatalf("request count = %d, want 3", len(bodies))
	}
	for i, body := range bodies {
		if body != `{"type":"validatorSummaries"}` {
			t.Fatalf("attempt %d body = %q", i+1, body)
		}
	}
	for i, body := range closes {
		if !body.closed.Load() {
			t.Fatalf("response body %d was not closed before return", i+1)
		}
	}
}

func TestValidatorCacheFreshStaleOutageAndRecovery(t *testing.T) {
	resolver, err := NewResolver("testnet")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_800_000_000, 0)
	now := base
	resolver.now = func() time.Time { return now }
	resolver.wait = func(context.Context, time.Duration) error { return nil }

	var mode atomic.Int32
	var calls atomic.Int32
	resolver.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if mode.Load() == 1 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Status:     "503 Service Unavailable",
				Body:       io.NopCloser(strings.NewReader("unavailable")),
				Header:     make(http.Header),
			}, nil
		}
		name := "initial"
		if mode.Load() == 2 {
			name = "recovered"
		} else if mode.Load() == 3 {
			name = "invalid"
		}
		payload, marshalErr := json.Marshal([]ValidatorSummary{{
			Validator: "0x1111111111111111111111111111111111111111",
			Signer:    "0x2222222222222222222222222222222222222222",
			Name:      name,
		}})
		if marshalErr != nil {
			t.Fatalf("marshal response: %v", marshalErr)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(string(payload))),
			Header:     make(http.Header),
		}, nil
	})}

	first, err := resolver.GetValidatorSummaries(context.Background(), false)
	if err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	if first.FromCache || first.Stale || !first.LastSuccess.Equal(base) {
		t.Fatalf("unexpected initial result: %+v", first)
	}
	first.Summaries[0].Name = "caller-mutated"

	now = base.Add(30 * time.Second)
	fresh, err := resolver.GetValidatorSummaries(context.Background(), false)
	if err != nil {
		t.Fatalf("fresh cache: %v", err)
	}
	if !fresh.FromCache || fresh.Stale || fresh.RefreshError != nil || calls.Load() != 1 {
		t.Fatalf("unexpected fresh-cache result: %+v calls=%d", fresh, calls.Load())
	}
	if fresh.Summaries[0].Name != "initial" {
		t.Fatalf("caller mutation escaped into cache: %+v", fresh.Summaries)
	}

	mode.Store(1)
	now = base.Add(2 * time.Minute)
	stale, err := resolver.GetValidatorSummaries(context.Background(), false)
	if err != nil {
		t.Fatalf("stale fallback returned outer error: %v", err)
	}
	if !stale.FromCache || !stale.Stale || stale.RefreshError == nil || !stale.LastSuccess.Equal(base) {
		t.Fatalf("unexpected stale fallback: %+v", stale)
	}
	if stale.Summaries[0].Name != "initial" {
		t.Fatalf("stale fallback changed cached data: %+v", stale.Summaries)
	}

	now = base.Add(10 * time.Minute)
	again, err := resolver.GetValidatorSummaries(context.Background(), false)
	if err != nil {
		t.Fatalf("prolonged stale fallback returned outer error: %v", err)
	}
	if !again.Stale || !again.LastSuccess.Equal(base) {
		t.Fatalf("prolonged outage advanced last success: %+v", again)
	}

	resolver.SetValidatorSummariesValidator(func(summaries []ValidatorSummary) error {
		if summaries[0].Name == "invalid" {
			return io.ErrUnexpectedEOF
		}
		return nil
	})
	mode.Store(3)
	now = base.Add(11 * time.Minute)
	invalid, err := resolver.GetValidatorSummaries(context.Background(), false)
	if err != nil {
		t.Fatalf("invalid refresh with last-known-good cache returned outer error: %v", err)
	}
	if !invalid.Stale || invalid.RefreshError == nil || invalid.Summaries[0].Name != "initial" ||
		!invalid.LastSuccess.Equal(base) {
		t.Fatalf("invalid refresh replaced last-known-good cache: %+v", invalid)
	}

	resolver.SetValidatorSummariesValidator(nil)
	mode.Store(2)
	now = base.Add(12 * time.Minute)
	recovered, err := resolver.GetValidatorSummaries(context.Background(), false)
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if recovered.FromCache || recovered.Stale || !recovered.LastSuccess.Equal(now) {
		t.Fatalf("unexpected recovery result: %+v", recovered)
	}
	if recovered.Summaries[0].Name != "recovered" {
		t.Fatalf("recovery did not replace cache: %+v", recovered.Summaries)
	}
}

func TestValidatorCacheTreatsExplicitEmptySnapshotAsValid(t *testing.T) {
	resolver, err := NewResolver("testnet")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_800_000_000, 0)
	now := base
	resolver.now = func() time.Time { return now }
	resolver.wait = func(context.Context, time.Duration) error { return nil }

	var fail atomic.Bool
	var calls atomic.Int32
	resolver.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		if fail.Load() {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Status:     "503 Service Unavailable",
				Body:       io.NopCloser(strings.NewReader("unavailable")),
				Header:     make(http.Header),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader("[]")),
			Header:     make(http.Header),
		}, nil
	})}

	first, err := resolver.GetValidatorSummaries(context.Background(), false)
	if err != nil {
		t.Fatalf("initial empty refresh: %v", err)
	}
	if first.Summaries == nil || len(first.Summaries) != 0 || first.FromCache || first.Stale ||
		first.RefreshError != nil || !first.LastSuccess.Equal(base) {
		t.Fatalf("unexpected initial empty result: %+v", first)
	}

	now = base.Add(30 * time.Second)
	fresh, err := resolver.GetValidatorSummaries(context.Background(), false)
	if err != nil {
		t.Fatalf("fresh empty cache: %v", err)
	}
	if fresh.Summaries == nil || len(fresh.Summaries) != 0 || !fresh.FromCache || fresh.Stale ||
		fresh.RefreshError != nil || !fresh.LastSuccess.Equal(base) || calls.Load() != 1 {
		t.Fatalf("unexpected fresh empty-cache result: %+v calls=%d", fresh, calls.Load())
	}

	fail.Store(true)
	now = base.Add(2 * time.Minute)
	stale, err := resolver.GetValidatorSummaries(context.Background(), false)
	if err != nil {
		t.Fatalf("stale empty fallback returned outer error: %v", err)
	}
	if stale.Summaries == nil || len(stale.Summaries) != 0 || !stale.FromCache || !stale.Stale ||
		stale.RefreshError == nil || !stale.LastSuccess.Equal(base) || calls.Load() <= 1 {
		t.Fatalf("unexpected stale empty fallback: %+v calls=%d", stale, calls.Load())
	}
}

func TestValidatorSummaryRejectsMissingOrNullRequiredFields(t *testing.T) {
	valid := map[string]interface{}{
		"validator":       "",
		"signer":          "",
		"nRecentBlocks":   float64(0),
		"stake":           float64(0),
		"isJailed":        false,
		"unjailableAfter": float64(0),
		"isActive":        false,
	}

	marshal := func(t *testing.T, fields map[string]interface{}) []byte {
		t.Helper()
		body, err := json.Marshal(fields)
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}
		return body
	}

	var summary ValidatorSummary
	if err := json.Unmarshal(marshal(t, valid), &summary); err != nil {
		t.Fatalf("explicit required zero/false values rejected: %v", err)
	}

	for _, field := range []string{
		"validator",
		"signer",
		"nRecentBlocks",
		"stake",
		"isJailed",
		"unjailableAfter",
		"isActive",
	} {
		t.Run(field+" null", func(t *testing.T) {
			fields := make(map[string]interface{}, len(valid))
			for key, value := range valid {
				fields[key] = value
			}
			fields[field] = nil
			if err := json.Unmarshal(marshal(t, fields), &summary); err == nil {
				t.Fatalf("null required field %s was accepted", field)
			}
		})

		t.Run(field+" missing", func(t *testing.T) {
			fields := make(map[string]interface{}, len(valid)-1)
			for key, value := range valid {
				if key != field {
					fields[key] = value
				}
			}
			if err := json.Unmarshal(marshal(t, fields), &summary); err == nil {
				t.Fatalf("missing required field %s was accepted", field)
			}
		})
	}
}

func TestFetchValidatorSummariesDistinguishesNullFromEmptyArray(t *testing.T) {
	resolver, err := NewResolver("testnet")
	if err != nil {
		t.Fatal(err)
	}
	resolver.wait = func(context.Context, time.Duration) error { return nil }

	response := "null"
	resolver.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(response)),
			Header:     make(http.Header),
		}, nil
	})}

	if _, err := resolver.fetchValidatorSummaries(context.Background()); err == nil {
		t.Fatal("top-level null validator summary response was accepted")
	} else {
		var callErr *CallError
		if !errors.As(err, &callErr) || callErr.Stage != FailureSchema {
			t.Fatalf("null response error = %T %v, want schema CallError", err, err)
		}
	}

	response = "[]"
	summaries, err := resolver.fetchValidatorSummaries(context.Background())
	if err != nil {
		t.Fatalf("explicit empty validator summary response rejected: %v", err)
	}
	if summaries == nil || len(summaries) != 0 {
		t.Fatalf("explicit empty response = %#v, want non-nil empty slice", summaries)
	}
}

func TestInvalidRequiredFieldRetainsLastKnownGoodValidatorCache(t *testing.T) {
	resolver, err := NewResolver("testnet")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_800_000_000, 0)
	now := base
	resolver.now = func() time.Time { return now }
	resolver.wait = func(context.Context, time.Duration) error { return nil }

	response := `[{
		"validator":"0x1111111111111111111111111111111111111111",
		"signer":"0x2222222222222222222222222222222222222222",
		"nRecentBlocks":0,
		"stake":1,
		"isJailed":false,
		"unjailableAfter":0,
		"isActive":true
	}]`
	resolver.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(response)),
			Header:     make(http.Header),
		}, nil
	})}

	first, err := resolver.GetValidatorSummaries(context.Background(), false)
	if err != nil {
		t.Fatalf("seed last-known-good cache: %v", err)
	}
	if first.FromCache || !first.LastSuccess.Equal(base) || first.Summaries[0].Stake != 1 {
		t.Fatalf("unexpected seed result: %+v", first)
	}

	now = base.Add(2 * time.Minute)
	response = `[{
		"validator":"0x1111111111111111111111111111111111111111",
		"signer":"0x2222222222222222222222222222222222222222",
		"nRecentBlocks":0,
		"stake":null,
		"isJailed":false,
		"unjailableAfter":0,
		"isActive":true
	}]`
	stale, err := resolver.GetValidatorSummaries(context.Background(), false)
	if err != nil {
		t.Fatalf("invalid refresh with cache returned outer error: %v", err)
	}
	if !stale.FromCache || !stale.Stale || stale.RefreshError == nil ||
		!stale.LastSuccess.Equal(base) || stale.Summaries[0].Stake != 1 {
		t.Fatalf("invalid required field replaced last-known-good cache: %+v", stale)
	}
	var callErr *CallError
	if !errors.As(stale.RefreshError, &callErr) || callErr.Stage != FailureDecode {
		t.Fatalf("refresh error = %T %v, want decode CallError", stale.RefreshError, stale.RefreshError)
	}
}
