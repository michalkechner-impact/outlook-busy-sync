// Package sync copies busy blocks between two accounts for a single
// direction. Bidirectional sync is simply two runs with swapped from/to.
//
// The core invariant: events we create in the target are tagged with a
// SourceRef extended property of the form "<from-account>:<source-id>".
// This lets us recognise our own artifacts across runs, avoid duplicating
// them, and prevent sync loops (a block that originated elsewhere and
// landed in the source calendar via a reverse pair will be skipped when
// the forward pair runs).
package sync

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/michalkechner-impact/outlook-busy-sync/internal/config"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/graph"
)

// CalendarClient is the subset of graph.Client operations the engine uses.
// Defined as an interface so tests can use in-memory fakes.
type CalendarClient interface {
	ListEvents(ctx context.Context, start, end time.Time) ([]graph.Event, error)
	CreateEvent(ctx context.Context, e graph.Event) (graph.Event, error)
	UpdateEvent(ctx context.Context, id string, e graph.Event) (graph.Event, error)
	DeleteEvent(ctx context.Context, id string) error
}

// Clients maps account names to their calendar clients.
type Clients map[string]CalendarClient

// Stats summarises one RunPair invocation.
type Stats struct {
	Fetched int
	Created int
	Updated int
	Deleted int
	Skipped int
	Errors  int
}

// Engine is a stateless orchestrator; all state lives in Graph via extended
// properties.
type Engine struct {
	clients Clients
	log     *slog.Logger
	now     func() time.Time // injectable for tests
}

// New creates an Engine.
func New(clients Clients, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{clients: clients, log: logger, now: time.Now}
}

// RunPair executes a single one-way sync. A non-nil error signals that
// the caller should treat this pair as failed (non-zero exit), including
// the case where individual event operations failed partway through —
// a silent "2 out of 5 creates succeeded" return would let a scheduled
// cron job report success while the target calendar drifted out of sync.
func (e *Engine) RunPair(ctx context.Context, pair config.ResolvedPair) (Stats, error) {
	src, ok := e.clients[pair.From]
	if !ok {
		return Stats{}, fmt.Errorf("unknown source account %q", pair.From)
	}
	dst, ok := e.clients[pair.To]
	if !ok {
		return Stats{}, fmt.Errorf("unknown target account %q", pair.To)
	}

	start := e.now().Add(-time.Duration(pair.LookbackDays) * 24 * time.Hour)
	end := e.now().Add(time.Duration(pair.LookaheadDays) * 24 * time.Hour)

	log := e.log.With(
		slog.String("from", pair.From),
		slog.String("to", pair.To),
		slog.Bool("dry_run", pair.DryRun),
	)
	log.Info("listing source events", slog.Time("from", start), slog.Time("to", end))
	srcEvents, err := src.ListEvents(ctx, start, end)
	if err != nil {
		return Stats{}, fmt.Errorf("list source events: %w", err)
	}
	log.Info("listing target events")
	dstEvents, err := dst.ListEvents(ctx, start, end)
	if err != nil {
		return Stats{}, fmt.Errorf("list target events: %w", err)
	}

	srcPrefix := pair.From + ":"
	reversePrefix := pair.To + ":" // events that originated from the target via the reverse sync pair

	// Filter source to events that should be reflected.
	var reflectable []graph.Event
	stats := Stats{Fetched: len(srcEvents)}
	for _, ev := range srcEvents {
		if skip, reason := shouldSkipSource(ev, pair, reversePrefix); skip {
			log.Debug("skip source", slog.String("subject", ev.Subject), slog.String("reason", reason))
			stats.Skipped++
			continue
		}
		reflectable = append(reflectable, ev)
	}

	// Index target events we own for this pair.
	ownedByRef := map[string]graph.Event{}
	for _, ev := range dstEvents {
		if strings.HasPrefix(ev.SourceRef, srcPrefix) {
			ownedByRef[ev.SourceRef] = ev
		}
	}

	// Plan + apply create/update.
	wantedRefs := map[string]struct{}{}
	for _, srcEv := range reflectable {
		ref := srcPrefix + srcEv.ID
		wantedRefs[ref] = struct{}{}
		want := shape(srcEv, pair, ref)
		if have, ok := ownedByRef[ref]; ok {
			if equalShape(have, want) {
				continue
			}
			log.Info("update", slog.String("subject", "(busy block)"), slog.Time("start", want.Start))
			if pair.DryRun {
				stats.Updated++
				continue
			}
			if _, err := dst.UpdateEvent(ctx, have.ID, want); err != nil {
				log.Error("update failed", slog.String("err", err.Error()))
				stats.Errors++
				continue
			}
			stats.Updated++
		} else {
			log.Info("create", slog.Time("start", want.Start), slog.Time("end", want.End))
			if pair.DryRun {
				stats.Created++
				continue
			}
			if _, err := dst.CreateEvent(ctx, want); err != nil {
				log.Error("create failed", slog.String("err", err.Error()))
				stats.Errors++
				continue
			}
			stats.Created++
		}
	}

	// Delete target events whose source has disappeared.
	for ref, ev := range ownedByRef {
		if _, keep := wantedRefs[ref]; keep {
			continue
		}
		log.Info("delete", slog.String("ref", ref))
		if pair.DryRun {
			stats.Deleted++
			continue
		}
		if err := dst.DeleteEvent(ctx, ev.ID); err != nil {
			log.Error("delete failed", slog.String("err", err.Error()))
			stats.Errors++
			continue
		}
		stats.Deleted++
	}

	log.Info("sync complete",
		slog.Int("fetched", stats.Fetched),
		slog.Int("created", stats.Created),
		slog.Int("updated", stats.Updated),
		slog.Int("deleted", stats.Deleted),
		slog.Int("skipped", stats.Skipped),
		slog.Int("errors", stats.Errors),
	)
	if stats.Errors > 0 {
		return stats, fmt.Errorf("%d event operation(s) failed", stats.Errors)
	}
	return stats, nil
}

// shouldSkipSource returns true if ev should not be mirrored to the target,
// along with a short human-readable reason.
func shouldSkipSource(ev graph.Event, pair config.ResolvedPair, reversePrefix string) (bool, string) {
	if ev.IsCancelled {
		return true, "cancelled"
	}
	if strings.HasPrefix(ev.SourceRef, reversePrefix) {
		// This event was itself placed here by our reverse sync. Reflecting
		// it back would create a loop.
		return true, "loop-guard"
	}
	if ev.ShowAs == "free" {
		return true, "showAs=free"
	}
	if pair.SkipAllDay && ev.IsAllDay {
		return true, "all-day"
	}
	if pair.SkipDeclined && ev.ResponseType == "declined" {
		return true, "declined"
	}
	return false, ""
}

// shape derives the target-side event we want to have in place of src.
func shape(src graph.Event, pair config.ResolvedPair, ref string) graph.Event {
	return graph.Event{
		Subject:   pair.Title,
		Start:     src.Start,
		End:       src.End,
		IsAllDay:  src.IsAllDay,
		ShowAs:    "busy",
		SourceRef: ref,
	}
}

// equalShape compares the fields we write when updating. Graph returns extra
// metadata (ID, timestamps) that we do not care about here.
func equalShape(have, want graph.Event) bool {
	if !have.Start.Equal(want.Start) || !have.End.Equal(want.End) {
		return false
	}
	if have.Subject != want.Subject {
		return false
	}
	if have.IsAllDay != want.IsAllDay {
		return false
	}
	if have.ShowAs != want.ShowAs {
		return false
	}
	return true
}
