package graph

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeTokens struct{ tok string }

func (f fakeTokens) Token(ctx context.Context) (string, error) { return f.tok, nil }

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := New(fakeTokens{tok: "t"})
	// Swap the base URL by proxying through the test server.
	c.httpClient = srv.Client()
	return c, srv
}

func TestParseGraphTime(t *testing.T) {
	got := parseGraphTime(rawDateTime{DateTime: "2026-04-13T09:00:00.0000000", TimeZone: "UTC"})
	want := time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestNormalize_extendedProps(t *testing.T) {
	r := rawEvent{
		ID:      "abc",
		Subject: "Busy",
		ExtendedProps: []rawExtProp{
			{ID: FullPropID, Value: "work:src123"},
			{ID: "String {other} Name Foo", Value: "ignored"},
		},
	}
	e := r.normalize()
	if e.SourceRef != "work:src123" {
		t.Errorf("SourceRef = %q", e.SourceRef)
	}
}

func TestEncodeWrite_includesExtendedProp(t *testing.T) {
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
	if !ok || len(props) != 1 {
		t.Fatalf("extendedProperties missing: %v", body)
	}
	p := props[0].(map[string]any)
	if p["value"] != "work:abc" {
		t.Errorf("unexpected prop value %v", p)
	}
	if !strings.HasPrefix(p["id"].(string), "String {"+SyncPropGUID+"} Name ") {
		t.Errorf("unexpected prop id %v", p)
	}
}

func TestDoJSON_retriesOn429(t *testing.T) {
	var count int
	_, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		if count < 2 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"error":"throttled"}`, http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	defer srv.Close()
	c := New(fakeTokens{tok: "t"})
	c.httpClient = srv.Client()
	var out map[string]any
	if err := c.doJSON(context.Background(), "GET", srv.URL, nil, &out); err != nil {
		t.Fatalf("doJSON: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 attempts, got %d", count)
	}
}

func TestDoJSON_authError(t *testing.T) {
	_, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	})
	defer srv.Close()
	c := New(fakeTokens{tok: "t"})
	c.httpClient = srv.Client()
	err := c.doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if !IsAuthError(err) {
		t.Errorf("expected auth error, got %v", err)
	}
}
