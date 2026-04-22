// Package graph is a narrow Microsoft Graph client for calendar operations.
// It implements exactly the endpoints needed for busy-block sync and nothing
// more, on purpose - a reviewer should be able to read it in one sitting.
package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	baseURL = "https://graph.microsoft.com/v1.0"

	// SyncPropGUID is a random namespace owned by this tool, scoping the
	// single-value extended property we use to tag synced events. Do not
	// change it across releases - existing synced events would become
	// orphaned.
	SyncPropGUID = "a6f9b3c8-2e41-4f1c-9b3d-8f2e41c9b3d7"
	// SyncPropName is the name of the extended property carrying the
	// "source:id" reference back to the originating event.
	SyncPropName = "SourceEventRef"
	// FullPropID is the "PropertyId" string Graph API uses for filtering /
	// expanding extended properties.
	FullPropID = "String {" + SyncPropGUID + "} Name " + SyncPropName
)

// TokenSource produces a fresh access token on demand. The auth package
// implements this with MSAL refresh tokens; tests can stub it.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Client is a thin Graph API wrapper bound to a single account.
type Client struct {
	tokens     TokenSource
	httpClient *http.Client
}

// New returns a Client that authenticates with ts.
func New(ts TokenSource) *Client {
	return &Client{
		tokens:     ts,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Event is a normalized representation of a Graph calendar event.
type Event struct {
	ID           string
	Subject      string
	Start        time.Time // UTC
	End          time.Time // UTC
	IsAllDay     bool
	ShowAs       string // "free", "tentative", "busy", "oof", "workingElsewhere", "unknown"
	IsCancelled  bool
	ResponseType string // "none", "organizer", "tentativelyAccepted", "accepted", "declined", "notResponded"
	// SourceRef, when non-empty, identifies this event as a sync artifact.
	// Format: "<account>:<source-event-id>". Only populated for events
	// created/owned by this tool.
	SourceRef string
}

// ListEvents returns all events between start and end from the primary
// calendar, with recurring series expanded to individual instances.
func (c *Client) ListEvents(ctx context.Context, start, end time.Time) ([]Event, error) {
	q := url.Values{}
	q.Set("startDateTime", start.UTC().Format(time.RFC3339))
	q.Set("endDateTime", end.UTC().Format(time.RFC3339))
	q.Set("$top", "200")
	q.Set("$select", "id,subject,start,end,isAllDay,showAs,isCancelled,responseStatus")
	q.Set("$expand", "singleValueExtendedProperties($filter=id eq '"+FullPropID+"')")

	endpoint := baseURL + "/me/calendarView?" + q.Encode()
	var out []Event
	for endpoint != "" {
		var resp struct {
			Value    []rawEvent `json:"value"`
			NextLink string     `json:"@odata.nextLink"`
		}
		if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Value {
			out = append(out, r.normalize())
		}
		endpoint = resp.NextLink
	}
	return out, nil
}

// CreateEvent creates a new event. SourceRef, if set, is persisted as an
// extended property so future sync runs can recognise the event as ours.
func (c *Client) CreateEvent(ctx context.Context, e Event) (Event, error) {
	body, err := encodeWrite(e)
	if err != nil {
		return Event{}, err
	}
	var got rawEvent
	if err := c.doJSON(ctx, http.MethodPost, baseURL+"/me/events", body, &got); err != nil {
		return Event{}, err
	}
	return got.normalize(), nil
}

// UpdateEvent patches start/end/subject/showAs on an existing event.
func (c *Client) UpdateEvent(ctx context.Context, id string, e Event) (Event, error) {
	body, err := encodeWrite(e)
	if err != nil {
		return Event{}, err
	}
	var got rawEvent
	if err := c.doJSON(ctx, http.MethodPatch, baseURL+"/me/events/"+url.PathEscape(id), body, &got); err != nil {
		return Event{}, err
	}
	return got.normalize(), nil
}

// DeleteEvent removes an event by ID.
func (c *Client) DeleteEvent(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodDelete, baseURL+"/me/events/"+url.PathEscape(id), nil, nil)
}

// --- internals ---

type rawEvent struct {
	ID             string        `json:"id,omitempty"`
	Subject        string        `json:"subject"`
	Start          rawDateTime   `json:"start"`
	End            rawDateTime   `json:"end"`
	IsAllDay       bool          `json:"isAllDay"`
	ShowAs         string        `json:"showAs"`
	IsCancelled    bool          `json:"isCancelled,omitempty"`
	ResponseStatus *rawResponse  `json:"responseStatus,omitempty"`
	ExtendedProps  []rawExtProp  `json:"singleValueExtendedProperties,omitempty"`
}

type rawDateTime struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

type rawResponse struct {
	Response string `json:"response"`
}

type rawExtProp struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

func (r rawEvent) normalize() Event {
	e := Event{
		ID:          r.ID,
		Subject:     r.Subject,
		IsAllDay:    r.IsAllDay,
		ShowAs:      r.ShowAs,
		IsCancelled: r.IsCancelled,
	}
	e.Start = parseGraphTime(r.Start)
	e.End = parseGraphTime(r.End)
	if r.ResponseStatus != nil {
		e.ResponseType = r.ResponseStatus.Response
	}
	for _, p := range r.ExtendedProps {
		if p.ID == FullPropID {
			e.SourceRef = p.Value
		}
	}
	return e
}

func parseGraphTime(dt rawDateTime) time.Time {
	if dt.DateTime == "" {
		return time.Time{}
	}
	// Graph returns "2026-04-13T09:00:00.0000000" with a separate timeZone
	// field. We parse as the named zone (usually "UTC" for calendarView).
	loc, err := time.LoadLocation(dt.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	// Try a few layouts Graph has been observed to return.
	for _, layout := range []string{
		"2006-01-02T15:04:05.0000000",
		"2006-01-02T15:04:05",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.ParseInLocation(layout, dt.DateTime, loc); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func encodeWrite(e Event) (io.Reader, error) {
	body := map[string]any{
		"subject":  e.Subject,
		"isAllDay": e.IsAllDay,
		"showAs":   e.ShowAs,
		"start": map[string]string{
			"dateTime": e.Start.UTC().Format("2006-01-02T15:04:05"),
			"timeZone": "UTC",
		},
		"end": map[string]string{
			"dateTime": e.End.UTC().Format("2006-01-02T15:04:05"),
			"timeZone": "UTC",
		},
		// Prevent meeting attendees from being auto-populated and prevent
		// reminder popups cluttering the user's notifications.
		"isReminderOn": false,
	}
	if e.SourceRef != "" {
		body["singleValueExtendedProperties"] = []map[string]string{
			{"id": FullPropID, "value": e.SourceRef},
		}
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(buf), nil
}

// doJSON issues an authenticated request. If body is nil, no body is sent.
// If out is nil, the response body is discarded. Non-2xx responses are
// returned as *APIError.
func (c *Client) doJSON(ctx context.Context, method, endpoint string, body io.Reader, out any) error {
	const maxAttempts = 4
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := c.newRequest(ctx, method, endpoint, body)
		if err != nil {
			return err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(backoff(attempt))
			continue
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = &APIError{Status: resp.StatusCode, Body: string(data)}
			sleep := retryAfter(resp) + backoff(attempt)
			time.Sleep(sleep)
			// Re-seek body for retry.
			if br, ok := body.(*bytes.Reader); ok {
				_, _ = br.Seek(0, io.SeekStart)
			}
			continue
		}
		if resp.StatusCode >= 400 {
			return &APIError{Status: resp.StatusCode, Body: string(data)}
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("decode %s: %w", endpoint, err)
			}
		}
		return nil
	}
	return lastErr
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	tok, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Preferring UTC in calendarView responses removes the need to juggle
	// per-event timezone strings in ListEvents.
	req.Header.Set("Prefer", `outlook.timezone="UTC"`)
	return req, nil
}

func backoff(attempt int) time.Duration {
	base := time.Duration(1<<attempt) * time.Second
	if base > 10*time.Second {
		base = 10 * time.Second
	}
	return base
}

func retryAfter(resp *http.Response) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if s, err := strconv.Atoi(v); err == nil {
			return time.Duration(s) * time.Second
		}
	}
	return 0
}

// APIError is returned for non-2xx Graph responses.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("graph api error %d: %s", e.Status, e.Body)
}

// IsAuthError reports whether err is a 401/403 from Graph.
func IsAuthError(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden
	}
	return false
}
