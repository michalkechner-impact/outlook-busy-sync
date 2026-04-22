package sync

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/michalkechner-impact/outlook-busy-sync/internal/config"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/graph"
)

type fakeClient struct {
	name   string
	events []graph.Event
	nextID int
	ops    []string // "create <subject>@<start>", "update <id>", "delete <id>"
}

func (f *fakeClient) ListEvents(ctx context.Context, start, end time.Time) ([]graph.Event, error) {
	return f.events, nil
}

func (f *fakeClient) CreateEvent(ctx context.Context, e graph.Event) (graph.Event, error) {
	f.nextID++
	e.ID = fmt.Sprintf("%s-%d", f.name, f.nextID)
	f.events = append(f.events, e)
	f.ops = append(f.ops, fmt.Sprintf("create %s@%s", e.Subject, e.Start.Format(time.RFC3339)))
	return e, nil
}

func (f *fakeClient) UpdateEvent(ctx context.Context, id string, e graph.Event) (graph.Event, error) {
	for i, ev := range f.events {
		if ev.ID == id {
			e.ID = id
			f.events[i] = e
			break
		}
	}
	f.ops = append(f.ops, "update "+id)
	return e, nil
}

func (f *fakeClient) DeleteEvent(ctx context.Context, id string) error {
	for i, ev := range f.events {
		if ev.ID == id {
			f.events = append(f.events[:i], f.events[i+1:]...)
			break
		}
	}
	f.ops = append(f.ops, "delete "+id)
	return nil
}

func defaultPair() config.ResolvedPair {
	return config.ResolvedPair{
		From:          "work",
		To:            "client",
		LookbackDays:  1,
		LookaheadDays: 30,
		Title:         "Busy",
		SkipDeclined:  true,
	}
}

func engineWith(src, dst *fakeClient) *Engine {
	e := New(Clients{"work": src, "client": dst}, slog.Default())
	e.now = func() time.Time { return time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC) }
	return e
}

func TestRunPair_createsBusyBlocks(t *testing.T) {
	src := &fakeClient{name: "work", events: []graph.Event{
		{ID: "s1", Subject: "Secret leadership sync", Start: tm(2026, 4, 14, 10), End: tm(2026, 4, 14, 11), ShowAs: "busy", ResponseType: "accepted"},
		{ID: "s2", Subject: "1:1 with boss", Start: tm(2026, 4, 15, 14), End: tm(2026, 4, 15, 15), ShowAs: "busy", ResponseType: "accepted"},
	}}
	dst := &fakeClient{name: "client"}
	stats, err := engineWith(src, dst).RunPair(context.Background(), defaultPair())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Created != 2 {
		t.Errorf("Created=%d want 2", stats.Created)
	}
	for _, ev := range dst.events {
		if ev.Subject != "Busy" {
			t.Errorf("subject %q leaked to target", ev.Subject)
		}
		if ev.ShowAs != "busy" {
			t.Errorf("showAs %q", ev.ShowAs)
		}
		if ev.SourceRef == "" {
			t.Errorf("missing SourceRef")
		}
	}
}

func TestRunPair_idempotent(t *testing.T) {
	src := &fakeClient{name: "work", events: []graph.Event{
		{ID: "s1", Subject: "x", Start: tm(2026, 4, 14, 10), End: tm(2026, 4, 14, 11), ShowAs: "busy", ResponseType: "accepted"},
	}}
	dst := &fakeClient{name: "client"}
	eng := engineWith(src, dst)
	if _, err := eng.RunPair(context.Background(), defaultPair()); err != nil {
		t.Fatal(err)
	}
	// Second run must not create duplicates.
	stats, err := eng.RunPair(context.Background(), defaultPair())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Created != 0 || stats.Updated != 0 {
		t.Errorf("second run made changes: %+v", stats)
	}
}

func TestRunPair_updatesOnTimeChange(t *testing.T) {
	src := &fakeClient{name: "work", events: []graph.Event{
		{ID: "s1", Start: tm(2026, 4, 14, 10), End: tm(2026, 4, 14, 11), ShowAs: "busy", ResponseType: "accepted"},
	}}
	dst := &fakeClient{name: "client"}
	eng := engineWith(src, dst)
	_, _ = eng.RunPair(context.Background(), defaultPair())

	// Source event moves by an hour.
	src.events[0].Start = tm(2026, 4, 14, 11)
	src.events[0].End = tm(2026, 4, 14, 12)

	stats, _ := eng.RunPair(context.Background(), defaultPair())
	if stats.Updated != 1 {
		t.Errorf("want 1 update, got %+v", stats)
	}
}

func TestRunPair_deletesWhenSourceGone(t *testing.T) {
	src := &fakeClient{name: "work", events: []graph.Event{
		{ID: "s1", Start: tm(2026, 4, 14, 10), End: tm(2026, 4, 14, 11), ShowAs: "busy", ResponseType: "accepted"},
	}}
	dst := &fakeClient{name: "client"}
	eng := engineWith(src, dst)
	_, _ = eng.RunPair(context.Background(), defaultPair())

	src.events = nil // meeting cancelled / removed from source

	stats, _ := eng.RunPair(context.Background(), defaultPair())
	if stats.Deleted != 1 {
		t.Errorf("want 1 delete, got %+v", stats)
	}
	if len(dst.events) != 0 {
		t.Errorf("target should be empty, has %d", len(dst.events))
	}
}

func TestRunPair_skipsDeclinedCancelledFreeAllDay(t *testing.T) {
	pair := defaultPair()
	pair.SkipAllDay = true
	src := &fakeClient{name: "work", events: []graph.Event{
		{ID: "a", Start: tm(2026, 4, 14, 9), End: tm(2026, 4, 14, 10), ShowAs: "busy", ResponseType: "declined"},
		{ID: "b", Start: tm(2026, 4, 15, 9), End: tm(2026, 4, 15, 10), ShowAs: "busy", IsCancelled: true},
		{ID: "c", Start: tm(2026, 4, 16, 9), End: tm(2026, 4, 16, 10), ShowAs: "free"},
		{ID: "d", Start: tm(2026, 4, 17, 0), End: tm(2026, 4, 18, 0), ShowAs: "busy", IsAllDay: true},
	}}
	dst := &fakeClient{name: "client"}
	stats, err := engineWith(src, dst).RunPair(context.Background(), pair)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Created != 0 {
		t.Errorf("nothing should be created, got %+v", stats)
	}
	if stats.Skipped != 4 {
		t.Errorf("want 4 skipped, got %+v", stats)
	}
}

func TestRunPair_loopGuard(t *testing.T) {
	// Simulate the reverse pair already having placed an event in the
	// source calendar. When we sync source → destination, we must not
	// reflect that synced-in event back onto the other side.
	src := &fakeClient{name: "work", events: []graph.Event{
		{ID: "real", Start: tm(2026, 4, 14, 10), End: tm(2026, 4, 14, 11), ShowAs: "busy", ResponseType: "accepted"},
		{ID: "ghost", Start: tm(2026, 4, 14, 12), End: tm(2026, 4, 14, 13), ShowAs: "busy", SourceRef: "client:origin123"},
	}}
	dst := &fakeClient{name: "client"}
	stats, err := engineWith(src, dst).RunPair(context.Background(), defaultPair())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Created != 1 {
		t.Errorf("only the real event should be mirrored, got %+v", stats)
	}
	if stats.Skipped != 1 {
		t.Errorf("ghost should have been skipped, got %+v", stats)
	}
}

func TestRunPair_doesNotTouchUnrelatedTargetEvents(t *testing.T) {
	src := &fakeClient{name: "work", events: []graph.Event{
		{ID: "s1", Start: tm(2026, 4, 14, 10), End: tm(2026, 4, 14, 11), ShowAs: "busy", ResponseType: "accepted"},
	}}
	dst := &fakeClient{name: "client", events: []graph.Event{
		{ID: "own-client-meeting", Subject: "Client standup", Start: tm(2026, 4, 14, 9), End: tm(2026, 4, 14, 9, 30), ShowAs: "busy"},
	}}
	eng := engineWith(src, dst)
	_, _ = eng.RunPair(context.Background(), defaultPair())
	// Second run: remove source, ensure the target's own native event is NOT deleted.
	src.events = nil
	_, _ = eng.RunPair(context.Background(), defaultPair())
	found := false
	for _, ev := range dst.events {
		if ev.ID == "own-client-meeting" {
			found = true
		}
	}
	if !found {
		t.Error("engine deleted an event it did not own")
	}
}

func tm(y, mo, d, h int, rest ...int) time.Time {
	minute := 0
	if len(rest) > 0 {
		minute = rest[0]
	}
	return time.Date(y, time.Month(mo), d, h, minute, 0, 0, time.UTC)
}
