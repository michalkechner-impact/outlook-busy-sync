package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michalkechner-impact/outlook-busy-sync/internal/auth"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/config"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/state"
)

// A precondition whose program is vpnDown fails, any other passes.
const (
	vpnDown = "/vpn/down"
	vpnUp   = "/vpn/up"
)

// stubPreconditions replaces the precondition runner and records what ran.
func stubPreconditions(t *testing.T) *[][]string {
	t.Helper()
	var ran [][]string
	prev := precondition
	precondition = func(_ context.Context, argv []string) error {
		ran = append(ran, argv)
		if argv[0] == vpnDown {
			return fmt.Errorf("%w: down: exit status 1", errPreconditionUnmet)
		}
		return nil
	}
	t.Cleanup(func() { precondition = prev })
	return &ran
}

// probeLog builds token sources from per-account errors and records which
// accounts got a source and how often each was asked for a token.
type probeLog struct {
	errs    map[string]error
	built   map[string]int
	tokens  map[string]int
	refused map[string]bool // building or probing these fails the test
	t       *testing.T
}

func newProbeLog(t *testing.T, errs map[string]error, refused ...string) *probeLog {
	l := &probeLog{errs: errs, built: map[string]int{}, tokens: map[string]int{}, refused: map[string]bool{}, t: t}
	for _, name := range refused {
		l.refused[name] = true
	}
	return l
}

type loggedSource struct {
	name string
	log  *probeLog
}

func (s loggedSource) Token(context.Context) (string, error) {
	s.log.tokens[s.name]++
	if s.log.refused[s.name] {
		s.log.t.Errorf("account %s was asked for a token although its precondition failed", s.name)
	}
	if err := s.log.errs[s.name]; err != nil {
		return "", err
	}
	return "token", nil
}

func (l *probeLog) newSource(acc config.Account) (tokenSource, error) {
	l.built[acc.Name]++
	if l.refused[acc.Name] {
		l.t.Errorf("token store for %s was opened although its precondition failed", acc.Name)
	}
	return loggedSource{name: acc.Name, log: l}, nil
}

func twoAccounts(workPre []string) *config.Config {
	return &config.Config{
		Accounts: []config.Account{
			{Name: "work", TenantID: "common", Precondition: workPre},
			{Name: "client", TenantID: "common"},
		},
		SyncPairs: []config.SyncPair{{From: "work", To: "client"}, {From: "client", To: "work"}},
	}
}

func TestSync_unmetPreconditionSkipsWithoutSignIn(t *testing.T) {
	ran := stubPreconditions(t)
	probes := newProbeLog(t, nil, "work")
	cfg := twoAccounts([]string{vpnDown, "state"})

	results, err := syncPairsWith(context.Background(), cfg, false, probes.newSource)

	if ExitCode(err) != ExitSkipped {
		t.Fatalf("want exit %d, got %d (%v)", ExitSkipped, ExitCode(err), err)
	}
	if !strings.Contains(err.Error(), "2 of 2 sync pair(s) skipped") || !strings.Contains(err.Error(), "account work: precondition not met") {
		t.Errorf("error should say what was skipped and why: %v", err)
	}
	if len(*ran) != 1 || (*ran)[0][0] != vpnDown {
		t.Errorf("want the work precondition run once, ran %q", *ran)
	}
	// Both pairs need work, so no account is probed at all.
	if len(probes.tokens) != 0 {
		t.Errorf("no token may be requested when every pair is skipped, got %v", probes.tokens)
	}
	if len(results) != 2 {
		t.Fatalf("want a result per pair, got %+v", results)
	}
	for _, r := range results {
		if r.Reason != state.ReasonPreconditionUnmet || !strings.HasPrefix(r.Error, "skipped: account work: precondition not met") {
			t.Errorf("pair %s->%s: want a skipped result, got %+v", r.From, r.To, r)
		}
	}

	// The skipped run is recorded, but it is not a success.
	path := filepath.Join(t.TempDir(), state.FileName)
	first := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	recordSyncState(path, false, first, cfg.SyncPairs, okResults(), nil)
	second := first.Add(15 * time.Minute)
	recordSyncState(path, false, second, cfg.SyncPairs, results, err)
	got := mustLoad(t, path)
	if !got.LastAttempt.Equal(second) || got.LastSuccess == nil || !got.LastSuccess.Equal(first) {
		t.Errorf("want attempt %v and last success still %v, got %v / %v", second, first, got.LastAttempt, got.LastSuccess)
	}
	if got.Pairs[0].Reason != state.ReasonPreconditionUnmet {
		t.Errorf("state must keep the reason, got %+v", got.Pairs[0])
	}
}

func TestSync_unmetPreconditionLeavesOtherPairs(t *testing.T) {
	stubPreconditions(t)
	// home is not logged in, which stops the run before any Graph call.
	probes := newProbeLog(t, map[string]error{"home": auth.ErrLoginRequired}, "work")
	cfg := &config.Config{
		Accounts: []config.Account{
			{Name: "work", TenantID: "common", Precondition: []string{vpnDown}},
			{Name: "client", TenantID: "common"},
			{Name: "home", TenantID: "common"},
		},
		SyncPairs: []config.SyncPair{{From: "work", To: "client"}, {From: "client", To: "home"}},
	}

	_, err := syncPairsWith(context.Background(), cfg, false, probes.newSource)

	if ExitCode(err) != ExitAuth || !strings.Contains(err.Error(), "home   used by: client→home") {
		t.Fatalf("want the missing login for home, got %d: %v", ExitCode(err), err)
	}
	if probes.tokens["client"] != 1 || probes.tokens["home"] != 1 {
		t.Errorf("the pair without work must still be prepared, tokens: %v", probes.tokens)
	}
	if probes.built["work"] != 0 {
		t.Errorf("work must not get a token store, built: %v", probes.built)
	}
}

func TestSync_metPreconditionUsesAccount(t *testing.T) {
	ran := stubPreconditions(t)
	probes := newProbeLog(t, map[string]error{"client": auth.ErrLoginRequired})

	_, err := syncPairsWith(context.Background(), twoAccounts([]string{vpnUp}), false, probes.newSource)

	if ExitCode(err) != ExitAuth {
		t.Fatalf("want the missing login for client, got %d: %v", ExitCode(err), err)
	}
	if len(*ran) != 1 || probes.tokens["work"] != 1 {
		t.Errorf("a met precondition must leave the account in use: ran %q, tokens %v", *ran, probes.tokens)
	}
}

// Still allowed: a config without preconditions runs exactly as before.
func TestSync_withoutPreconditionUnchanged(t *testing.T) {
	ran := stubPreconditions(t)
	probes := newProbeLog(t, map[string]error{"client": auth.ErrLoginRequired})

	results, err := syncPairsWith(context.Background(), twoAccounts(nil), false, probes.newSource)

	if len(*ran) != 0 {
		t.Errorf("no precondition configured, but ran %q", *ran)
	}
	if ExitCode(err) != ExitAuth || results != nil {
		t.Errorf("missing login: want exit %d and no results, got %d, %+v", ExitAuth, ExitCode(err), results)
	}
	if probes.tokens["work"] != 1 || probes.tokens["client"] != 1 || probes.built["work"] != 1 || probes.built["client"] != 1 {
		t.Errorf("every used account is set up and probed once: built %v, tokens %v", probes.built, probes.tokens)
	}
}

func caBlocked() error {
	return errors.New("silent token: AADSTS53003: access has been blocked by Conditional Access policies")
}

func TestSync_policyBlockMarksPairs(t *testing.T) {
	stubPreconditions(t)
	probes := newProbeLog(t, map[string]error{"work": caBlocked()})
	cfg := &config.Config{
		Accounts: []config.Account{
			{Name: "work", TenantID: "common"},
			{Name: "client", TenantID: "common"},
			{Name: "home", TenantID: "common"},
		},
		SyncPairs: []config.SyncPair{{From: "work", To: "client"}, {From: "client", To: "home"}},
	}

	results, err := syncPairsWith(context.Background(), cfg, false, probes.newSource)

	if ExitCode(err) != ExitAuth {
		t.Fatalf("a refused sign-in stays an auth failure, got %d: %v", ExitCode(err), err)
	}
	if len(results) != 2 {
		t.Fatalf("want a result per pair, got %+v", results)
	}
	if results[0].Reason != state.ReasonBlockedByPolicy || !strings.HasPrefix(results[0].Error, "auth probe failed") {
		t.Errorf("work->client: want blocked_by_policy, got %+v", results[0])
	}
	if results[1].Reason != "" || results[1].Error != results[0].Error {
		t.Errorf("client->home: want the same error without a reason, got %+v", results[1])
	}
}

func TestStatus_unmetPreconditionSkipsProbe(t *testing.T) {
	stubPreconditions(t)
	probes := newProbeLog(t, nil, "work")
	cfg := twoAccounts([]string{vpnDown})

	r := buildStatus(context.Background(), cfg, "/cfg/config.yaml", filepath.Join(t.TempDir(), state.FileName), probes.newSource)
	got := decodeStatus(t, r)

	if got.Accounts[0].Auth != authPreconditionUnmet || !strings.Contains(got.Accounts[0].AuthError, "precondition not met") {
		t.Errorf("work: want precondition_unmet with the reason, got %+v", got.Accounts[0])
	}
	if got.Accounts[1].Auth != authLoggedIn {
		t.Errorf("client: want logged_in, got %+v", got.Accounts[1])
	}
	if probes.built["work"] != 0 || probes.tokens["work"] != 0 {
		t.Errorf("work must not be probed: built %v, tokens %v", probes.built, probes.tokens)
	}
}

func TestStatus_policyBlock(t *testing.T) {
	stubPreconditions(t)
	probes := newProbeLog(t, map[string]error{"work": caBlocked()})

	r := buildStatus(context.Background(), twoAccounts(nil), "/cfg/config.yaml", filepath.Join(t.TempDir(), state.FileName), probes.newSource)
	got := decodeStatus(t, r)

	if got.Accounts[0].Auth != authBlockedByPolicy || !strings.Contains(got.Accounts[0].AuthError, "AADSTS53003") {
		t.Errorf("work: want blocked_by_policy with the error, got %+v", got.Accounts[0])
	}
}
