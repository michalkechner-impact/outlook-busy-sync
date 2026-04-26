package graph

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeTokens struct {
	tok string
	err error
}

func (f fakeTokens) Token(ctx context.Context) (string, error) { return f.tok, f.err }

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := New(fakeTokens{tok: "t"})
	c.httpClient = srv.Client()
	c.SetBaseURL(srv.URL)
	t.Cleanup(srv.Close)
	return c, srv
}

func TestParseGraphTime_happy(t *testing.T) {
	got, err := parseGraphTime(rawDateTime{DateTime: "2026-04-13T09:00:00.0000000", TimeZone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestParseGraphTime_errorsOnEmpty(t *testing.T) {
	if _, err := parseGraphTime(rawDateTime{}); err == nil {
		t.Error("empty dateTime must error, not silently return year 0001")
	}
}

func TestParseGraphTime_errorsOnGarbage(t *testing.T) {
	if _, err := parseGraphTime(rawDateTime{DateTime: "not-a-date", TimeZone: "UTC"}); err == nil {
		t.Error("unparseable dateTime must error")
	}
}

func TestParseGraphTime_unknownTimezoneFallsBackToUTC(t *testing.T) {
	// Windows zone name that Go's tzdata does not know. The Prefer header
	// should prevent this in practice but defence in depth matters.
	got, err := parseGraphTime(rawDateTime{DateTime: "2026-04-13T09:00:00", TimeZone: "Pacific Standard Time"})
	if err != nil {
		t.Fatal(err)
	}
	// Unknown zone falls back to UTC, so the parsed instant is 09:00 UTC.
	if got.Hour() != 9 || got.Location() != time.UTC {
		t.Errorf("unknown tz should fall back to UTC: got %v", got)
	}
}

func TestNormalize_extendedProps(t *testing.T) {
	r := rawEvent{
		ID:      "abc",
		Subject: "Busy",
		Start:   rawDateTime{DateTime: "2026-04-13T09:00:00", TimeZone: "UTC"},
		End:     rawDateTime{DateTime: "2026-04-13T10:00:00", TimeZone: "UTC"},
		ExtendedProps: []rawExtProp{
			{ID: FullPropID, Value: "work:src123"},
			{ID: "String {other} Name Foo", Value: "ignored"},
		},
	}
	e, err := r.normalize()
	if err != nil {
		t.Fatal(err)
	}
	if e.SourceRef != "work:src123" {
		t.Errorf("SourceRef = %q", e.SourceRef)
	}
}

func TestNormalize_errorsOnMissingStart(t *testing.T) {
	// Guards against the "year 0001 busy block" class of silent failure.
	r := rawEvent{ID: "abc", Subject: "Busy"}
	if _, err := r.normalize(); err == nil {
		t.Error("missing start/end must propagate an error")
	}
}

func TestEncodeWrite_includesExtendedProp(t *testing.T) {
	// Both extended properties (SourceEventRef + MirrorBodyHash) are always
	// emitted when SourceRef is set. This is required so a mirror→busy
	// downgrade clears MirrorHash to "" instead of leaving the stale value
	// behind, which would loop equalShape into endless updates.
	r, err := encodeWrite(Event{
		Subject:   "Busy",
		Start:     time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC),
		End:       time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC),
		ShowAs:    "busy",
		SourceRef: "work:abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	props, ok := body["singleValueExtendedProperties"].([]any)
	if !ok || len(props) != 2 {
		t.Fatalf("expected SourceRef + MirrorHash extended props, got: %v", body)
	}
	gotByID := map[string]string{}
	for _, raw := range props {
		p := raw.(map[string]any)
		gotByID[p["id"].(string)] = p["value"].(string)
	}
	if gotByID[FullPropID] != "work:abc" {
		t.Errorf("SourceRef value mismatch: %v", gotByID)
	}
	// In busy mode the mirror hash must be emitted as "" so any prior hash
	// on the target is overwritten.
	if v, ok := gotByID[FullMirrorHashID]; !ok || v != "" {
		t.Errorf("MirrorHash must be emitted as empty in busy mode; got %v", gotByID)
	}
}

func TestEncodeWrite_busyModeAlwaysClearsMirrorContent(t *testing.T) {
	// Privacy regression: a mirror→busy downgrade must blank body, location,
	// and reset sensitivity to "normal" on the wire. Previously these were
	// only emitted when non-empty, which silently preserved mirror content
	// in target events that flipped back to busy.
	r, err := encodeWrite(Event{
		Subject:   "Busy",
		Start:     time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC),
		End:       time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC),
		ShowAs:    "busy",
		SourceRef: "work:abc",
		// Body / Location / Sensitivity / MirrorHash all empty.
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	bodyField := body["body"].(map[string]any)
	if bodyField["content"] != "" {
		t.Errorf("body content must be cleared on busy write; got %q", bodyField["content"])
	}
	loc := body["location"].(map[string]any)
	if loc["displayName"] != "" {
		t.Errorf("location must be cleared on busy write; got %q", loc["displayName"])
	}
	if body["sensitivity"] != "normal" {
		t.Errorf("sensitivity must be reset to 'normal' on busy write; got %v", body["sensitivity"])
	}
}

func TestListEvents_followsNextLinkAndPreservesSourceRef(t *testing.T) {
	// Regression for the "pagination silently drops events" concern.
	// Page 1 contains an owned event (with SourceRef); page 2 contains an
	// unowned event. Both must survive the pagination.
	var srvURL string
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/me/calendarView") && !strings.Contains(r.URL.RawQuery, "page=2") {
			// Page 1
			resp := map[string]any{
				"value": []map[string]any{{
					"id":       "e1",
					"subject":  "mine",
					"start":    map[string]string{"dateTime": "2026-04-13T09:00:00", "timeZone": "UTC"},
					"end":      map[string]string{"dateTime": "2026-04-13T10:00:00", "timeZone": "UTC"},
					"showAs":   "busy",
					"singleValueExtendedProperties": []map[string]string{
						{"id": FullPropID, "value": "work:srcA"},
					},
				}},
				"@odata.nextLink": srvURL + "/me/calendarView?page=2",
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		// Page 2
		resp := map[string]any{
			"value": []map[string]any{{
				"id":      "e2",
				"subject": "theirs",
				"start":   map[string]string{"dateTime": "2026-04-13T11:00:00", "timeZone": "UTC"},
				"end":     map[string]string{"dateTime": "2026-04-13T12:00:00", "timeZone": "UTC"},
				"showAs":  "busy",
			}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
	c, srv := newTestClient(t, handler)
	srvURL = srv.URL

	events, err := c.ListEvents(context.Background(), time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events across pages, got %d", len(events))
	}
	if events[0].SourceRef != "work:srcA" {
		t.Errorf("page-1 SourceRef lost: %q", events[0].SourceRef)
	}
	if events[1].SourceRef != "" {
		t.Errorf("page-2 event should not have a SourceRef, got %q", events[1].SourceRef)
	}
}

func TestListEvents_requestShape(t *testing.T) {
	var gotQuery string
	var gotPrefer string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotPrefer = r.Header.Get("Prefer")
		_, _ = w.Write([]byte(`{"value":[]}`))
	})
	_, err := c.ListEvents(context.Background(), time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// Pager cap is load-bearing — changing this will regress the Graph
	// pagination-with-$expand quirk.
	if !strings.Contains(gotQuery, "%24top=25") {
		t.Errorf("listPageSize should be 25 in query, got %s", gotQuery)
	}
	if !strings.Contains(gotQuery, "%24expand=singleValueExtendedProperties") {
		t.Errorf("$expand should be present, got %s", gotQuery)
	}
	if !strings.Contains(gotPrefer, `outlook.timezone="UTC"`) {
		t.Errorf("Prefer header missing UTC: %q", gotPrefer)
	}
}

func TestDoJSON_retriesOn429(t *testing.T) {
	var count int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&count, 1) < 2 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"error":"throttled"}`, http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	var out map[string]any
	if err := c.doJSON(context.Background(), "GET", c.baseURL, nil, &out); err != nil {
		t.Fatalf("doJSON: %v", err)
	}
	if n := atomic.LoadInt32(&count); n != 2 {
		t.Errorf("expected 2 attempts, got %d", n)
	}
}

func TestDoJSON_doesNotRetryPOSTOn5xx(t *testing.T) {
	// Retrying a POST on 5xx is a duplicate-event hazard: the server may
	// have committed the write before responding. We only retry writes on
	// explicit 429.
	var count int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		http.Error(w, `{"error":"boom"}`, http.StatusBadGateway)
	})
	body := strings.NewReader(`{"x":1}`)
	err := c.doJSON(context.Background(), "POST", c.baseURL, body, nil)
	if err == nil {
		t.Fatal("expected error on 502")
	}
	if n := atomic.LoadInt32(&count); n != 1 {
		t.Errorf("POST must not retry 5xx, got %d attempts", n)
	}
}

func TestDoJSON_retriesGETOn5xx(t *testing.T) {
	var count int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&count, 1) < 2 {
			http.Error(w, `{"error":"boom"}`, http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	var out map[string]any
	if err := c.doJSON(context.Background(), "GET", c.baseURL, nil, &out); err != nil {
		t.Fatalf("GET retry: %v", err)
	}
	if n := atomic.LoadInt32(&count); n != 2 {
		t.Errorf("GET should retry 5xx once, got %d attempts", n)
	}
}

func TestDoJSON_respectsContextDuringBackoff(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		http.Error(w, `{"error":"throttled"}`, http.StatusTooManyRequests)
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := c.doJSON(ctx, "GET", c.baseURL, nil, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	// Must bail within ~250ms, not wait the full 30s Retry-After.
	if elapsed > time.Second {
		t.Errorf("sleepCtx did not honour context: elapsed %v", elapsed)
	}
}

func TestDoJSON_authError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	})
	err := c.doJSON(context.Background(), "GET", c.baseURL, nil, nil)
	if !IsAuthError(err) {
		t.Errorf("expected auth error, got %v", err)
	}
}

func TestDoJSON_tokenFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request must not be issued when token fetch fails")
	}))
	defer srv.Close()
	c := New(fakeTokens{err: errTokenForTest{}})
	c.SetBaseURL(srv.URL)
	err := c.doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("token error must propagate")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("expected token error, got %v", err)
	}
}

type errTokenForTest struct{}

func (errTokenForTest) Error() string { return "token source failed" }

func TestRetryAfter_parsesHTTPDate(t *testing.T) {
	// RFC 7231 allows either "delay-seconds" or an HTTP-date. We need to
	// honour the date form or we hammer Graph ignoring its explicit
	// back-off directive.
	resp := &http.Response{Header: http.Header{}}
	future := time.Now().Add(5 * time.Second)
	resp.Header.Set("Retry-After", future.UTC().Format(http.TimeFormat))
	d := retryAfter(resp)
	if d <= 0 || d > 10*time.Second {
		t.Errorf("retryAfter HTTP-date: got %v, want ~5s", d)
	}
}

func TestTruncate_capsBody(t *testing.T) {
	long := strings.Repeat("x", 1000)
	got := truncate(long, 100)
	if !strings.Contains(got, "...[truncated]") || len(got) > 120 {
		t.Errorf("truncate didn't cap: len=%d", len(got))
	}
	short := "short"
	if truncate(short, 100) != short {
		t.Error("truncate must pass through short strings")
	}
}
