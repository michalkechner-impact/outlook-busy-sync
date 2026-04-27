package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/michalkechner-impact/outlook-busy-sync/internal/config"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/graph"
)

func mirrorPair() config.ResolvedPair {
	p := defaultPair()
	p.Mode = config.ModeMirror
	return p
}

func TestMirror_copiesSubjectLocationBodyAndMarksPrivate(t *testing.T) {
	src := &fakeClient{name: "work", events: []graph.Event{{
		ID:        "s1",
		Subject:   "Q2 planning",
		Start:     tm(2026, 4, 14, 10),
		End:       tm(2026, 4, 14, 11),
		ShowAs:    "busy",
		Location:  "Room 7",
		Organizer: "jane@work.com",
		Attendees: []string{"adrian@work.com", "peter@work.com"},
		Body:      "Agenda: roadmap, hiring",
	}}}
	dst := &fakeClient{name: "client"}
	stats, err := engineWith(src, dst).RunPair(context.Background(), mirrorPair())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Created != 1 {
		t.Fatalf("Created=%d want 1", stats.Created)
	}
	got := dst.events[0]
	if got.Subject != "Q2 planning" {
		t.Errorf("subject not mirrored: %q", got.Subject)
	}
	if got.Sensitivity != "private" {
		t.Errorf("must be marked private; got %q", got.Sensitivity)
	}
	if got.Location != "Room 7" {
		t.Errorf("location not mirrored: %q", got.Location)
	}
	if !strings.Contains(got.Body, "Organizer: jane@work.com") {
		t.Errorf("body missing organizer; body=%q", got.Body)
	}
	for _, want := range []string{"adrian@work.com", "peter@work.com", "Q2 planning", "Room 7", "Agenda: roadmap, hiring", "[synced from work"} {
		if !strings.Contains(got.Body, want) {
			t.Errorf("body missing %q; body=%q", want, got.Body)
		}
	}
	if got.MirrorHash == "" {
		t.Errorf("mirror hash must be set on target; got empty")
	}
}

func TestMirror_copiesReminderFromSource(t *testing.T) {
	src := &fakeClient{name: "work", events: []graph.Event{{
		ID:                         "s1",
		Subject:                    "Daily standup",
		Start:                      tm(2026, 4, 14, 10),
		End:                        tm(2026, 4, 14, 11),
		ShowAs:                     "busy",
		IsReminderOn:               true,
		ReminderMinutesBeforeStart: 15,
	}}}
	dst := &fakeClient{name: "client"}
	if _, err := engineWith(src, dst).RunPair(context.Background(), mirrorPair()); err != nil {
		t.Fatal(err)
	}
	got := dst.events[0]
	if !got.IsReminderOn {
		t.Errorf("mirror must copy IsReminderOn=true from source so the target tenant pings the user before the meeting")
	}
	if got.ReminderMinutesBeforeStart != 15 {
		t.Errorf("mirror must copy reminder minutes; got %d want 15", got.ReminderMinutesBeforeStart)
	}
}

func TestMirror_respectsSourceReminderOffSetting(t *testing.T) {
	// If the source meeting has reminders disabled, mirror must not turn
	// them on — we copy intent, never impose.
	src := &fakeClient{name: "work", events: []graph.Event{{
		ID:           "s1",
		Subject:      "Silent block",
		Start:        tm(2026, 4, 14, 10),
		End:          tm(2026, 4, 14, 11),
		ShowAs:       "busy",
		IsReminderOn: false,
	}}}
	dst := &fakeClient{name: "client"}
	if _, err := engineWith(src, dst).RunPair(context.Background(), mirrorPair()); err != nil {
		t.Fatal(err)
	}
	if dst.events[0].IsReminderOn {
		t.Errorf("mirror must NOT enable reminders when source has them disabled")
	}
}

func TestMirror_reminderChangeUpdatesHash(t *testing.T) {
	// Reminder fields are part of the canonical mirror payload; changing the
	// source's reminder time must cause the next sync to PATCH the target.
	src := &fakeClient{name: "work", events: []graph.Event{{
		ID:                         "s1",
		Subject:                    "x",
		Start:                      tm(2026, 4, 14, 10),
		End:                        tm(2026, 4, 14, 11),
		ShowAs:                     "busy",
		IsReminderOn:               true,
		ReminderMinutesBeforeStart: 15,
	}}}
	dst := &fakeClient{name: "client"}
	eng := engineWith(src, dst)
	_, _ = eng.RunPair(context.Background(), mirrorPair())

	src.events[0].ReminderMinutesBeforeStart = 60

	stats, err := eng.RunPair(context.Background(), mirrorPair())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Updated != 1 {
		t.Errorf("reminder change in source must trigger hash drift + update; got %+v", stats)
	}
	if dst.events[0].ReminderMinutesBeforeStart != 60 {
		t.Errorf("target reminder minutes not refreshed: %d", dst.events[0].ReminderMinutesBeforeStart)
	}
}

func TestMirror_doesNotPopulateStructuredAttendees(t *testing.T) {
	// Critical privacy / spam guard: mirror must NEVER write attendees as
	// structured fields, only as text inside body. Otherwise the second
	// tenant will send fresh meeting invitations from the mirrored event.
	src := &fakeClient{name: "work", events: []graph.Event{{
		ID:        "s1",
		Subject:   "x",
		Start:     tm(2026, 4, 14, 10),
		End:       tm(2026, 4, 14, 11),
		ShowAs:    "busy",
		Attendees: []string{"adrian@work.com"},
	}}}
	dst := &fakeClient{name: "client"}
	if _, err := engineWith(src, dst).RunPair(context.Background(), mirrorPair()); err != nil {
		t.Fatal(err)
	}
	if len(dst.events[0].Attendees) != 0 {
		t.Errorf("mirror must not write structured attendees; would spam invites: %v", dst.events[0].Attendees)
	}
}

func TestMirror_idempotentViaHash(t *testing.T) {
	// Outlook may rewrite our body on save — Subject and timing stay stable.
	// equalShape() must not trigger an update purely because the round-tripped
	// body string differs, as long as the stored MirrorHash matches what the
	// source would produce.
	src := &fakeClient{name: "work", events: []graph.Event{{
		ID:        "s1",
		Subject:   "Roadmap review",
		Start:     tm(2026, 4, 14, 10),
		End:       tm(2026, 4, 14, 11),
		ShowAs:    "busy",
		Body:      "agenda",
		Organizer: "jane@work.com",
	}}}
	dst := &fakeClient{name: "client"}
	eng := engineWith(src, dst)
	if _, err := eng.RunPair(context.Background(), mirrorPair()); err != nil {
		t.Fatal(err)
	}
	// Simulate Outlook normalising the body (e.g. adding HTML wrappers when
	// it round-trips through the server). The hash extended property is
	// unchanged because Outlook can't see / rewrite it.
	dst.events[0].Body = "<html><body>agenda</body></html>"

	stats, err := eng.RunPair(context.Background(), mirrorPair())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Updated != 0 {
		t.Errorf("hash-equal mirror must not trigger update: %+v", stats)
	}
}

func TestMirror_updatesWhenSourceContentChanges(t *testing.T) {
	src := &fakeClient{name: "work", events: []graph.Event{{
		ID: "s1", Subject: "old", Start: tm(2026, 4, 14, 10), End: tm(2026, 4, 14, 11), ShowAs: "busy",
	}}}
	dst := &fakeClient{name: "client"}
	eng := engineWith(src, dst)
	_, _ = eng.RunPair(context.Background(), mirrorPair())

	// Source title changed → hash differs → must update.
	src.events[0].Subject = "new"

	stats, err := eng.RunPair(context.Background(), mirrorPair())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Updated != 1 {
		t.Errorf("subject change in source must update target: %+v", stats)
	}
	if dst.events[0].Subject != "new" {
		t.Errorf("target subject not refreshed: %q", dst.events[0].Subject)
	}
}

func TestMirror_upgradesExistingBusyBlock(t *testing.T) {
	// User flips an existing pair from busy → mirror. Pre-existing target
	// events from the busy run have no MirrorHash set; the next mirror run
	// must update them in place to enrich content.
	src := &fakeClient{name: "work", events: []graph.Event{{
		ID: "s1", Subject: "Real title", Start: tm(2026, 4, 14, 10), End: tm(2026, 4, 14, 11), ShowAs: "busy",
	}}}
	// Pre-seeded "Busy" placeholder from a prior busy-mode run.
	dst := &fakeClient{name: "client", events: []graph.Event{{
		ID: "tgt-1", Subject: "Busy", Start: tm(2026, 4, 14, 10), End: tm(2026, 4, 14, 11),
		ShowAs: "busy", SourceRef: "work:s1",
	}}}
	stats, err := engineWith(src, dst).RunPair(context.Background(), mirrorPair())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Updated != 1 {
		t.Errorf("existing busy block should be upgraded to mirror: %+v", stats)
	}
	if dst.events[0].Subject != "Real title" {
		t.Errorf("upgrade did not enrich subject: %q", dst.events[0].Subject)
	}
	if dst.events[0].Sensitivity != "private" {
		t.Errorf("upgrade must mark target private: %q", dst.events[0].Sensitivity)
	}
}

func TestMirror_busyDefaultUnchanged(t *testing.T) {
	// Sanity: mirror code path must not affect the busy default. This is the
	// privacy contract that all docs and the README depend on. Includes a
	// guard that source reminder settings do not bleed into busy-mode targets
	// — busy blocks must stay silent regardless of source reminder state.
	src := &fakeClient{name: "work", events: []graph.Event{{
		ID:                         "s1",
		Subject:                    "Sensitive sync",
		Start:                      tm(2026, 4, 14, 10),
		End:                        tm(2026, 4, 14, 11),
		ShowAs:                     "busy",
		Location:                   "Room 7",
		Body:                       "agenda",
		Attendees:                  []string{"adrian@work.com"},
		IsReminderOn:               true,
		ReminderMinutesBeforeStart: 15,
	}}}
	dst := &fakeClient{name: "client"}
	if _, err := engineWith(src, dst).RunPair(context.Background(), defaultPair()); err != nil {
		t.Fatal(err)
	}
	got := dst.events[0]
	if got.Subject != "Busy" {
		t.Errorf("busy default leaked subject: %q", got.Subject)
	}
	if got.IsReminderOn || got.ReminderMinutesBeforeStart != 0 {
		t.Errorf("busy default must keep reminders off; got isReminderOn=%t minutes=%d", got.IsReminderOn, got.ReminderMinutesBeforeStart)
	}
	if got.Body != "" || got.Location != "" || got.Sensitivity != "" || got.MirrorHash != "" {
		t.Errorf("busy default leaked content: body=%q loc=%q sens=%q hash=%q", got.Body, got.Location, got.Sensitivity, got.MirrorHash)
	}
}

func TestMirror_asymmetricPairsAreIndependent(t *testing.T) {
	// Threat model: ecco (client) must never receive mirror content from
	// impact (employer). Asymmetric configuration achieves this by setting
	// mode per pair. Verify both directions in one test.
	impactEvents := []graph.Event{{
		ID: "i1", Subject: "Internal Impact strategy",
		Start: tm(2026, 4, 14, 10), End: tm(2026, 4, 14, 11), ShowAs: "busy",
	}}
	eccoEvents := []graph.Event{{
		ID: "e1", Subject: "Ecco client work",
		Start: tm(2026, 4, 15, 10), End: tm(2026, 4, 15, 11), ShowAs: "busy",
	}}
	impact := &fakeClient{name: "impact", events: impactEvents}
	ecco := &fakeClient{name: "ecco", events: eccoEvents}

	eng := New(Clients{"impact": impact, "ecco": ecco}, nil)
	eng.now = engineWith(impact, ecco).now

	// ecco → impact: mirror (employer sees client work in detail).
	eccoToImpact := config.ResolvedPair{
		From: "ecco", To: "impact",
		LookbackDays: 1, LookaheadDays: 30, Title: "Busy",
		SkipDeclined: true, Mode: config.ModeMirror,
	}
	if _, err := eng.RunPair(context.Background(), eccoToImpact); err != nil {
		t.Fatal(err)
	}

	// impact → ecco: busy (client must see only opaque blocks).
	impactToEcco := config.ResolvedPair{
		From: "impact", To: "ecco",
		LookbackDays: 1, LookaheadDays: 30, Title: "Busy",
		SkipDeclined: true, Mode: config.ModeBusy,
	}
	if _, err := eng.RunPair(context.Background(), impactToEcco); err != nil {
		t.Fatal(err)
	}

	// Find each created mirror/busy block. Both calendars now contain a
	// native event AND a synced event from the other side.
	var inImpact, inEcco *graph.Event
	for i := range impact.events {
		if impact.events[i].SourceRef == "ecco:e1" {
			inImpact = &impact.events[i]
		}
	}
	for i := range ecco.events {
		if ecco.events[i].SourceRef == "impact:i1" {
			inEcco = &ecco.events[i]
		}
	}
	if inImpact == nil {
		t.Fatal("ecco → impact mirror event not created")
	}
	if inEcco == nil {
		t.Fatal("impact → ecco busy event not created")
	}

	if inImpact.Subject != "Ecco client work" {
		t.Errorf("ecco→impact mirror should keep real subject; got %q", inImpact.Subject)
	}
	if inEcco.Subject != "Busy" {
		t.Errorf("impact→ecco busy must NOT leak subject; got %q", inEcco.Subject)
	}
	if inEcco.Body != "" || inEcco.Sensitivity != "" {
		t.Errorf("impact→ecco busy must leak no content; body=%q sens=%q", inEcco.Body, inEcco.Sensitivity)
	}
}

func TestMirror_downgradeToBusyClearsContentAndStops(t *testing.T) {
	// Bugbot-found regression: switching a pair from mirror back to busy
	// must clear body/location/sensitivity AND the MirrorHash extended
	// property on existing target events. Otherwise:
	//   1. Subject becomes "Busy" but body still leaks attendees-as-text.
	//   2. equalShape sees stale MirrorHash on target vs empty on the
	//      busy-mode want and churns updates indefinitely.
	src := &fakeClient{name: "work", events: []graph.Event{{
		ID:        "s1",
		Subject:   "Confidential",
		Start:     tm(2026, 4, 14, 10),
		End:       tm(2026, 4, 14, 11),
		ShowAs:    "busy",
		Location:  "Boardroom",
		Body:      "agenda",
		Organizer: "ceo@work.com",
		Attendees: []string{"adrian@work.com"},
	}}}
	dst := &fakeClient{name: "client"}
	eng := engineWith(src, dst)

	// First, run mirror to populate target with full content + hash.
	if _, err := eng.RunPair(context.Background(), mirrorPair()); err != nil {
		t.Fatal(err)
	}
	if dst.events[0].MirrorHash == "" {
		t.Fatal("setup: mirror run should have populated MirrorHash")
	}
	if dst.events[0].Body == "" || dst.events[0].Subject != "Confidential" {
		t.Fatal("setup: mirror should have populated body and real subject")
	}

	// Now run the same pair in busy mode — simulates user flipping back.
	stats, err := eng.RunPair(context.Background(), defaultPair())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Updated != 1 {
		t.Fatalf("downgrade should issue exactly one update; got %+v", stats)
	}
	got := dst.events[0]
	if got.Subject != "Busy" {
		t.Errorf("subject must revert to busy title; got %q", got.Subject)
	}
	if got.Body != "" {
		t.Errorf("body must be cleared on downgrade; still has %q", got.Body)
	}
	if got.Location != "" {
		t.Errorf("location must be cleared on downgrade; still has %q", got.Location)
	}
	if got.MirrorHash != "" {
		t.Errorf("MirrorHash must be cleared on downgrade; still has %q (would loop updates)", got.MirrorHash)
	}

	// Critical: a third run in busy mode must be a no-op. If MirrorHash
	// were not cleared, equalShape would see have.MirrorHash != "" while
	// want.MirrorHash == "" and trigger another update, every cycle.
	stats, err = eng.RunPair(context.Background(), defaultPair())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Updated != 0 {
		t.Errorf("after downgrade, busy mode must be idempotent; got %+v (update loop)", stats)
	}
}

func TestMirrorHash_stableAcrossAttendeeOrder(t *testing.T) {
	a := graph.Event{
		Subject:   "x",
		Start:     tm(2026, 4, 14, 10),
		End:       tm(2026, 4, 14, 11),
		Attendees: []string{"adrian@x.com", "peter@x.com"},
	}
	b := a
	b.Attendees = []string{"peter@x.com", "adrian@x.com"}
	if mirrorHash(a) != mirrorHash(b) {
		t.Error("attendee order must not change the hash")
	}
}

func TestMirrorHash_changesWhenContentChanges(t *testing.T) {
	a := graph.Event{Subject: "x", Start: tm(2026, 4, 14, 10), End: tm(2026, 4, 14, 11), Body: "agenda"}
	b := a
	b.Body = "different agenda"
	if mirrorHash(a) == mirrorHash(b) {
		t.Error("body change must update the hash")
	}
}
