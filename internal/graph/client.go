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
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is Graph API v1.0. Overridable on Client for tests.
	DefaultBaseURL = "https://graph.microsoft.com/v1.0"

	// SyncPropGUID is a random namespace owned by this tool, scoping the
	// single-value extended properties we use to tag synced events. Do not
	// change it across releases - existing synced events would become
	// orphaned.
	SyncPropGUID = "a6f9b3c8-2e41-4f1c-9b3d-8f2e41c9b3d7"
	// SyncPropName is the name of the extended property carrying the
	// "source:id" reference back to the originating event.
	SyncPropName = "SourceEventRef"
	// MirrorHashName carries a hex SHA-256 digest of the canonical mirror
	// payload. It lets equalShape() detect drift in mirror mode without
	// re-comparing free-form text fields that Outlook may rewrite on save.
	MirrorHashName = "MirrorBodyHash"
	// FullPropID is the "PropertyId" string Graph API uses for filtering /
	// expanding the SourceEventRef extended property.
	FullPropID = "String {" + SyncPropGUID + "} Name " + SyncPropName
	// FullMirrorHashID is the "PropertyId" string for the MirrorBodyHash
	// extended property (mirror-mode drift detection).
	FullMirrorHashID = "String {" + SyncPropGUID + "} Name " + MirrorHashName

	// listPageSize is the $top we request from calendarView. Graph has a
	// documented quirk where combining $expand on extended properties with
	// larger page sizes can silently drop events on paginated responses.
	// 25 matches the conservative limit Microsoft's own SDKs use for this
	// combination.
	listPageSize = 25
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
	// baseURL is exported via New's default; set directly in tests to
	// redirect traffic at an httptest server.
	baseURL string
}

// New returns a Client that authenticates with ts.
func New(ts TokenSource) *Client {
	return &Client{
		tokens:     ts,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    DefaultBaseURL,
	}
}

// SetBaseURL overrides the Graph base URL. Intended for tests.
func (c *Client) SetBaseURL(u string) { c.baseURL = u }

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

	// Mirror-mode fields. Populated by ListEvents from the source mailbox
	// and by encodeWrite onto target events. Organizer/Attendees are read
	// only — we never write them to the target, to avoid sending duplicate
	// meeting invitations from the second tenant.
	Body        string   // plain text only; HTML bodies are flattened on read
	Location    string   // free-form display string from Outlook
	Sensitivity string   // "normal", "private", "personal", "confidential"
	Organizer   string   // email of the source-side organizer
	Attendees   []string // emails of source-side attendees
	// IsReminderOn and ReminderMinutesBeforeStart drive Outlook's pre-meeting
	// notification. Busy-mode shapes leave these zeroed so the legacy
	// behaviour (no popups for opaque "Busy" blocks) is preserved; mirror
	// shape copies them from the source so a user running Outlook on the
	// target tenant gets reminders for the meetings whose source lives in
	// another tenant.
	IsReminderOn               bool
	ReminderMinutesBeforeStart int
	// MirrorHash, when non-empty on a target event, is the hex SHA-256 of
	// the canonical source payload that produced this mirror. Compared
	// instead of free-form fields to avoid update churn from Outlook's
	// silent body-HTML rewrites.
	MirrorHash string
}

// ListEvents returns all events between start and end from the primary
// calendar, with recurring series expanded to individual instances.
func (c *Client) ListEvents(ctx context.Context, start, end time.Time) ([]Event, error) {
	q := url.Values{}
	q.Set("startDateTime", start.UTC().Format(time.RFC3339))
	q.Set("endDateTime", end.UTC().Format(time.RFC3339))
	q.Set("$top", strconv.Itoa(listPageSize))
	q.Set("$select", "id,subject,start,end,isAllDay,showAs,isCancelled,responseStatus,body,location,sensitivity,organizer,attendees,isReminderOn,reminderMinutesBeforeStart")
	// Expand both extended properties (sync ref + mirror hash). Graph
	// requires the OR-filter form because $expand only takes one filter.
	q.Set("$expand", "singleValueExtendedProperties($filter=id eq '"+FullPropID+"' or id eq '"+FullMirrorHashID+"')")

	endpoint := c.baseURL + "/me/calendarView?" + q.Encode()
	var out []Event
	// Guard against a pathological Graph response that returns the same
	// @odata.nextLink forever. With listPageSize=25 and a reasonable sync
	// window this bounds us to tens of thousands of events.
	const maxPages = 1000
	for pages := 0; endpoint != "" && pages < maxPages; pages++ {
		var resp struct {
			Value    []rawEvent `json:"value"`
			NextLink string     `json:"@odata.nextLink"`
		}
		if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Value {
			ev, err := r.normalize()
			if err != nil {
				// A malformed event is logged by the caller via the error;
				// drop it from the result rather than creating a garbage
				// busy block at year 0001.
				return nil, fmt.Errorf("normalize event %q: %w", r.ID, err)
			}
			out = append(out, ev)
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
	if err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/me/events", body, &got); err != nil {
		return Event{}, err
	}
	return got.normalize()
}

// UpdateEvent patches start/end/subject/showAs on an existing event.
func (c *Client) UpdateEvent(ctx context.Context, id string, e Event) (Event, error) {
	body, err := encodeWrite(e)
	if err != nil {
		return Event{}, err
	}
	var got rawEvent
	if err := c.doJSON(ctx, http.MethodPatch, c.baseURL+"/me/events/"+url.PathEscape(id), body, &got); err != nil {
		return Event{}, err
	}
	return got.normalize()
}

// DeleteEvent removes an event by ID.
func (c *Client) DeleteEvent(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodDelete, c.baseURL+"/me/events/"+url.PathEscape(id), nil, nil)
}

// --- internals ---

type rawEvent struct {
	ID                         string        `json:"id,omitempty"`
	Subject                    string        `json:"subject"`
	Start                      rawDateTime   `json:"start"`
	End                        rawDateTime   `json:"end"`
	IsAllDay                   bool          `json:"isAllDay"`
	ShowAs                     string        `json:"showAs"`
	IsCancelled                bool          `json:"isCancelled,omitempty"`
	ResponseStatus             *rawResponse  `json:"responseStatus,omitempty"`
	Body                       *rawBody      `json:"body,omitempty"`
	Location                   *rawLocation  `json:"location,omitempty"`
	Sensitivity                string        `json:"sensitivity,omitempty"`
	Organizer                  *rawOrganizer `json:"organizer,omitempty"`
	Attendees                  []rawAttendee `json:"attendees,omitempty"`
	IsReminderOn               *bool         `json:"isReminderOn,omitempty"`
	ReminderMinutesBeforeStart *int          `json:"reminderMinutesBeforeStart,omitempty"`
	ExtendedProps              []rawExtProp  `json:"singleValueExtendedProperties,omitempty"`
}

type rawDateTime struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

type rawResponse struct {
	Response string `json:"response"`
}

type rawBody struct {
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

type rawLocation struct {
	DisplayName string `json:"displayName"`
}

type rawOrganizer struct {
	EmailAddress rawEmail `json:"emailAddress"`
}

type rawAttendee struct {
	EmailAddress rawEmail `json:"emailAddress"`
	Type         string   `json:"type,omitempty"`
}

type rawEmail struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

type rawExtProp struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

func (r rawEvent) normalize() (Event, error) {
	e := Event{
		ID:          r.ID,
		Subject:     r.Subject,
		IsAllDay:    r.IsAllDay,
		ShowAs:      r.ShowAs,
		IsCancelled: r.IsCancelled,
	}
	start, err := parseGraphTime(r.Start)
	if err != nil {
		return Event{}, fmt.Errorf("start: %w", err)
	}
	end, err := parseGraphTime(r.End)
	if err != nil {
		return Event{}, fmt.Errorf("end: %w", err)
	}
	e.Start = start
	e.End = end
	if r.ResponseStatus != nil {
		e.ResponseType = r.ResponseStatus.Response
	}
	if r.Body != nil {
		if r.Body.ContentType == "html" {
			e.Body = htmlToPlain(r.Body.Content)
		} else {
			e.Body = r.Body.Content
		}
	}
	if r.Location != nil {
		e.Location = r.Location.DisplayName
	}
	e.Sensitivity = r.Sensitivity
	if r.Organizer != nil {
		e.Organizer = r.Organizer.EmailAddress.Address
	}
	for _, a := range r.Attendees {
		if a.EmailAddress.Address != "" {
			e.Attendees = append(e.Attendees, a.EmailAddress.Address)
		}
	}
	if r.IsReminderOn != nil {
		e.IsReminderOn = *r.IsReminderOn
	}
	if r.ReminderMinutesBeforeStart != nil {
		e.ReminderMinutesBeforeStart = *r.ReminderMinutesBeforeStart
	}
	for _, p := range r.ExtendedProps {
		switch p.ID {
		case FullPropID:
			e.SourceRef = p.Value
		case FullMirrorHashID:
			e.MirrorHash = p.Value
		}
	}
	return e, nil
}

// parseGraphTime converts Graph's {dateTime, timeZone} pair to a UTC time.
// Returns an error rather than a zero value so callers cannot accidentally
// persist a year-0001 busy block.
func parseGraphTime(dt rawDateTime) (time.Time, error) {
	if dt.DateTime == "" {
		return time.Time{}, errors.New("empty dateTime")
	}
	// Graph returns "2026-04-13T09:00:00.0000000" with a separate timeZone
	// field. We parse as the named zone; if unknown (e.g. Windows zone
	// names sneak through when the Prefer: outlook.timezone header is
	// ignored), we fall back to UTC rather than fail — the dateTime is
	// the canonical part.
	loc, err := time.LoadLocation(dt.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.0000000",
		"2006-01-02T15:04:05",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.ParseInLocation(layout, dt.DateTime, loc); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable dateTime %q (tz=%q)", dt.DateTime, dt.TimeZone)
}

func encodeWrite(e Event) (io.Reader, error) {
	// Body, location, sensitivity, and the MirrorHash extended property are
	// ALWAYS written, even when their values are empty/default. A mirror →
	// busy downgrade flips them back to empty/normal/empty; if we omitted
	// empty fields here, Graph's PATCH semantics would preserve the prior
	// mirror content, leaking subject/attendees-as-text into a "Busy"
	// event and stranding the MirrorHash so equalShape would loop updates
	// forever.
	sensitivity := e.Sensitivity
	if sensitivity == "" {
		sensitivity = "normal"
	}
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
		"body":        map[string]string{"contentType": "text", "content": e.Body},
		"location":    map[string]string{"displayName": e.Location},
		"sensitivity": sensitivity,
		// Reminder fields are always emitted: in busy mode IsReminderOn is
		// false (no popups for opaque "Busy" blocks); in mirror mode the
		// shape copies the source's reminder settings so the user gets a
		// pre-meeting ping in the target tenant for events whose source
		// lives in another tenant. Empty values explicitly clear any prior
		// reminder configuration left over from a mode switch.
		"isReminderOn":               e.IsReminderOn,
		"reminderMinutesBeforeStart": e.ReminderMinutesBeforeStart,
	}
	// Always emit BOTH extended properties when this is a sync artifact, so
	// a mirror → busy downgrade clears MirrorHash to "" rather than leaving
	// the stale hash behind.
	if e.SourceRef != "" {
		body["singleValueExtendedProperties"] = []map[string]string{
			{"id": FullPropID, "value": e.SourceRef},
			{"id": FullMirrorHashID, "value": e.MirrorHash},
		}
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(buf), nil
}

// htmlToPlain is a deliberately minimal HTML stripper: we only need it to
// flatten Outlook's body content into something we can hash and re-display.
// It is not a security boundary and not robust against adversarial HTML.
var htmlTagRegexp = regexp.MustCompile(`(?s)<[^>]+>`)

// blockBoundaryRegexp matches the close-tag of common block elements. We
// substitute these with a literal newline before tag stripping so that
// "<p>foo</p><p>bar</p>" renders as "foo\nbar" instead of "foobar". Open
// tags like <br> are handled by the same alternation.
var blockBoundaryRegexp = regexp.MustCompile(`(?i)</(p|div|li|tr|h[1-6])>|<br\s*/?>`)

// teamsJoinURLPrefix is the literal prefix Microsoft uses for Teams meeting
// join URLs. We strip these from body content copied to the target tenant:
// clicking a Teams link from the wrong tenant joins the meeting as an
// external guest, which some organizers' policies block. Better to force
// the user back to the original invite.
const teamsJoinURLPrefix = "https://teams.microsoft.com/l/meetup-join/"

func htmlToPlain(s string) string {
	// Insert paragraph breaks at block-element boundaries before stripping
	// all tags, otherwise "<p>foo</p><p>bar</p>" collapses to "foobar".
	s = blockBoundaryRegexp.ReplaceAllString(s, "\n")
	s = htmlTagRegexp.ReplaceAllString(s, "")
	s = htmlEntityReplacer.Replace(s)
	// Collapse runs of whitespace but keep single newlines so paragraphs
	// stay visually separated.
	s = collapseWhitespace(s)
	return strings.TrimSpace(s)
}

var htmlEntityReplacer = strings.NewReplacer(
	"&nbsp;", " ",
	"&amp;", "&",
	"&lt;", "<",
	"&gt;", ">",
	"&quot;", `"`,
	"&#39;", "'",
)

var multiSpaceRegexp = regexp.MustCompile(`[ \t]+`)
var multiNewlineRegexp = regexp.MustCompile(`\n{3,}`)

func collapseWhitespace(s string) string {
	s = multiSpaceRegexp.ReplaceAllString(s, " ")
	s = multiNewlineRegexp.ReplaceAllString(s, "\n\n")
	return s
}

// StripTeamsJoinURL removes Microsoft Teams meeting join URLs from a string.
// A Teams URL runs from the well-known prefix to a terminator: whitespace,
// or one of the characters that cannot legally appear in a URL but commonly
// abuts one in real-world HTML-flattened text (quotes, angle brackets,
// parentheses). Exported for the sync engine to use when composing mirror
// bodies.
func StripTeamsJoinURL(s string) string {
	const replacement = "[Teams meeting link removed]"
	// Terminators: whitespace + the URL-illegal characters that show up
	// adjacent to URLs in HTML-flattened bodies. Without these, an anchor
	// like `<a href="...join/abc">click here</a>` flattened to
	// `...join/abcclick here` would consume "click here" along with the URL.
	const terminators = " \t\r\n<>\"'()"
	var out strings.Builder
	for {
		i := strings.Index(s, teamsJoinURLPrefix)
		if i < 0 {
			out.WriteString(s)
			return out.String()
		}
		out.WriteString(s[:i])
		out.WriteString(replacement)
		s = s[i+len(teamsJoinURLPrefix):]
		j := strings.IndexAny(s, terminators)
		if j < 0 {
			return out.String()
		}
		s = s[j:]
	}
}

// doJSON issues an authenticated request. If body is nil, no body is sent.
// If out is nil, the response body is discarded. Non-2xx responses are
// returned as *APIError.
func (c *Client) doJSON(ctx context.Context, method, endpoint string, body io.Reader, out any) error {
	const maxAttempts = 4
	// Only GET/DELETE are retried on 5xx: they are idempotent. POST and
	// PATCH are only retried on 429 (where Graph has explicitly told us
	// it did not accept the request), because a 5xx retry on a write risks
	// duplicating an event that in fact landed server-side before the
	// error was returned.
	retry5xx := method == http.MethodGet || method == http.MethodDelete

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Re-seek a seekable body to the start of each attempt so retries
		// send the full payload.
		if seeker, ok := body.(io.Seeker); ok && attempt > 0 {
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				return fmt.Errorf("seek body for retry: %w", err)
			}
		}
		req, err := c.newRequest(ctx, method, endpoint, body)
		if err != nil {
			return err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if err := sleepCtx(ctx, backoff(attempt)); err != nil {
				return err
			}
			continue
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("%s %s: read body: %w", method, endpoint, err)
			if err := sleepCtx(ctx, backoff(attempt)); err != nil {
				return err
			}
			continue
		}
		isTooMany := resp.StatusCode == http.StatusTooManyRequests
		is5xx := resp.StatusCode >= 500
		shouldRetry := isTooMany || (is5xx && retry5xx)
		if shouldRetry {
			lastErr = &APIError{Status: resp.StatusCode, Body: truncate(string(data), 512)}
			sleep := retryAfter(resp) + backoff(attempt)
			if err := sleepCtx(ctx, sleep); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode >= 400 {
			return &APIError{Status: resp.StatusCode, Body: truncate(string(data), 512)}
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

// sleepCtx blocks for d or until ctx is cancelled, whichever comes first.
// A scheduled `sync` task must be able to stop promptly when launchd /
// systemd / Task Scheduler tells it to.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func backoff(attempt int) time.Duration {
	base := time.Duration(1<<attempt) * time.Second
	if base > 10*time.Second {
		base = 10 * time.Second
	}
	return base
}

// retryAfter parses the Retry-After header in either seconds or HTTP-date
// form (both are permitted by RFC 7231). Unparseable values return 0 so
// the caller's backoff still applies.
func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil {
		if s < 0 {
			return 0
		}
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// truncate caps s at n runes and appends a marker. Used on APIError.Body
// so error logs don't splash potentially-tenant-identifying correlation
// IDs across our journal.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[truncated]"
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
