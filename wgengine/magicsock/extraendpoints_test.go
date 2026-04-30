// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package magicsock

import (
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"tailscale.com/envknob"
)

func TestExtraEndpointsMaxFromEnvDefault(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_EXTRA_ENDPOINTS_MAX", "")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_EXTRA_ENDPOINTS_MAX", "") })

	if got := extraEndpointsMaxFromEnv(); got != extraEndpointsDefaultMax {
		t.Fatalf("default = %d; want %d", got, extraEndpointsDefaultMax)
	}
}

func TestExtraEndpointsMaxFromEnvOverride(t *testing.T) {
	envknob.Setenv("TS_EXPERIMENTAL_EXTRA_ENDPOINTS_MAX", "5")
	t.Cleanup(func() { envknob.Setenv("TS_EXPERIMENTAL_EXTRA_ENDPOINTS_MAX", "") })

	if got := extraEndpointsMaxFromEnv(); got != 5 {
		t.Fatalf("override = %d; want 5", got)
	}
}

func TestAddrPortSlicesEqual(t *testing.T) {
	a := netip.MustParseAddrPort("1.2.3.4:41641")
	b := netip.MustParseAddrPort("5.6.7.8:41641")
	c := netip.MustParseAddrPort("9.10.11.12:41641")

	tests := []struct {
		name string
		x, y []netip.AddrPort
		want bool
	}{
		{"both empty", nil, nil, true},
		{"empty vs empty slice", nil, []netip.AddrPort{}, true},
		{"empty vs non-empty", nil, []netip.AddrPort{a}, false},
		{"same single", []netip.AddrPort{a}, []netip.AddrPort{a}, true},
		{"same set different order", []netip.AddrPort{a, b, c}, []netip.AddrPort{c, a, b}, true},
		{"different elements", []netip.AddrPort{a, b}, []netip.AddrPort{a, c}, false},
		{"different lengths", []netip.AddrPort{a, b}, []netip.AddrPort{a}, false},
		{"duplicates same set", []netip.AddrPort{a, a, b}, []netip.AddrPort{a, a, b}, true},
		{"duplicates different multiplicity", []netip.AddrPort{a, a, b}, []netip.AddrPort{a, b, b}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := addrPortSlicesEqual(tc.x, tc.y); got != tc.want {
				t.Errorf("addrPortSlicesEqual(%v, %v) = %v; want %v", tc.x, tc.y, got, tc.want)
			}
		})
	}
}

// newTestExtraEndpointsState returns a state with reload disabled (nil)
// and discardLog logf, suitable for direct parseAndApply tests.
func newTestExtraEndpointsState(path string, max int) *extraEndpointsState {
	return &extraEndpointsState{
		logf:   func(string, ...any) {},
		path:   path,
		max:    max,
		reload: nil,
		stopCh: make(chan struct{}),
	}
}

func TestExtraEndpointsParseAndApplyValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extra.json")
	if err := os.WriteFile(path, []byte(`{"endpoints":["1.2.3.4:41641","[2001:db8::1]:41641","5.6.7.8:42000"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newTestExtraEndpointsState(path, extraEndpointsDefaultMax)
	got, err := s.parseAndApply()
	if err != nil {
		t.Fatalf("parseAndApply: %v", err)
	}
	want := []netip.AddrPort{
		netip.MustParseAddrPort("1.2.3.4:41641"),
		netip.MustParseAddrPort("[2001:db8::1]:41641"),
		netip.MustParseAddrPort("5.6.7.8:42000"),
	}
	if !addrPortSlicesEqual(got, want) {
		t.Errorf("got %v; want %v", got, want)
	}
	if !addrPortSlicesEqual(s.snapshot(), want) {
		t.Errorf("snapshot mismatch: got %v; want %v", s.snapshot(), want)
	}
}

func TestExtraEndpointsParseAndApplyMissingFile(t *testing.T) {
	s := newTestExtraEndpointsState(filepath.Join(t.TempDir(), "does-not-exist.json"), extraEndpointsDefaultMax)
	got, err := s.parseAndApply()
	if err != nil {
		t.Fatalf("missing file should be treated as empty, not error; got %v", err)
	}
	if got != nil {
		t.Errorf("missing file: got %v; want nil", got)
	}
}

func TestExtraEndpointsParseAndApplyMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extra.json")
	if err := os.WriteFile(path, []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestExtraEndpointsState(path, extraEndpointsDefaultMax)
	if _, err := s.parseAndApply(); err == nil {
		t.Fatal("malformed JSON should return error")
	}
}

func TestExtraEndpointsParseAndApplyDropsInvalidEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extra.json")
	if err := os.WriteFile(path, []byte(`{"endpoints":["1.2.3.4:41641","not-an-addr","1.2.3.4:41641","9.10.11.12:42000"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestExtraEndpointsState(path, extraEndpointsDefaultMax)
	got, err := s.parseAndApply()
	if err != nil {
		t.Fatalf("parseAndApply: %v", err)
	}
	want := []netip.AddrPort{
		netip.MustParseAddrPort("1.2.3.4:41641"),
		netip.MustParseAddrPort("9.10.11.12:42000"),
	}
	if !addrPortSlicesEqual(got, want) {
		t.Errorf("expected dedup + drop-invalid; got %v want %v", got, want)
	}
}

func TestExtraEndpointsParseAndApplyEnforcesCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extra.json")
	if err := os.WriteFile(path, []byte(`{"endpoints":["1.2.3.4:1","1.2.3.4:2","1.2.3.4:3","1.2.3.4:4"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestExtraEndpointsState(path, 2) // cap = 2
	got, err := s.parseAndApply()
	if err != nil {
		t.Fatalf("parseAndApply: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected cap=2 to drop the rest; got %d entries: %v", len(got), got)
	}
}

func TestExtraEndpointsParseAndApplyRefusesGroupWritable(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "js" {
		t.Skip("permission check is unix-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "extra.json")
	if err := os.WriteFile(path, []byte(`{"endpoints":["1.2.3.4:41641"]}`), 0o660); err != nil {
		t.Fatal(err)
	}
	s := newTestExtraEndpointsState(path, extraEndpointsDefaultMax)
	_, err := s.parseAndApply()
	if err == nil {
		t.Fatal("group-writable file (0660) should be refused")
	}
	if !strings.Contains(err.Error(), "group-writable") && !strings.Contains(err.Error(), "world-writable") {
		t.Errorf("expected permission error; got %v", err)
	}
}

func TestExtraEndpointsParseAndApplyAcceptsRestrictivePerms(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "js" {
		t.Skip("permission check is unix-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "extra.json")
	for _, mode := range []os.FileMode{0o600, 0o640, 0o644, 0o400, 0o444} {
		if err := os.WriteFile(path, []byte(`{"endpoints":["1.2.3.4:41641"]}`), mode); err != nil {
			t.Fatal(err)
		}
		// Re-stat is not enough; ensure mode actually applied (umask
		// may strip bits, but our writes don't widen, so this is fine).
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		s := newTestExtraEndpointsState(path, extraEndpointsDefaultMax)
		if _, err := s.parseAndApply(); err != nil {
			t.Errorf("mode %o should be accepted; got %v", mode, err)
		}
	}
}

func TestExtraEndpointsParseAndApplyRefusesOversizedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extra.json")
	// Build a JSON file larger than extraEndpointsMaxFileSize. The
	// validity of the contents doesn't matter — the size check rejects
	// before parsing.
	big := strings.Repeat("a", extraEndpointsMaxFileSize+1)
	if err := os.WriteFile(path, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestExtraEndpointsState(path, extraEndpointsDefaultMax)
	_, err := s.parseAndApply()
	if err == nil {
		t.Fatal("oversized file should be refused")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected size error; got %v", err)
	}
}

// TestExtraEndpointsHandleFileChangeTriggersReloadOnDelta verifies the
// reload callback fires only when the parsed set actually changes.
func TestExtraEndpointsHandleFileChangeTriggersReloadOnDelta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extra.json")
	if err := os.WriteFile(path, []byte(`{"endpoints":["1.2.3.4:41641"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var reloads atomic.Int64
	s := &extraEndpointsState{
		logf:   func(string, ...any) {},
		path:   path,
		max:    extraEndpointsDefaultMax,
		reload: func() { reloads.Add(1) },
		stopCh: make(chan struct{}),
	}

	// First read populates s.cur.
	if _, err := s.parseAndApply(); err != nil {
		t.Fatal(err)
	}

	// handleFileChange with no real change → reload must not fire.
	s.handleFileChange()
	if got := reloads.Load(); got != 0 {
		t.Fatalf("reload fired %d times despite no change", got)
	}

	// Modify file → reload must fire exactly once.
	if err := os.WriteFile(path, []byte(`{"endpoints":["1.2.3.4:41641","5.6.7.8:42000"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s.handleFileChange()
	if got := reloads.Load(); got != 1 {
		t.Fatalf("reload fired %d times after change; want 1", got)
	}

	// Same file again, same parsed set → reload must not fire.
	s.handleFileChange()
	if got := reloads.Load(); got != 1 {
		t.Fatalf("reload fired %d times on no-op re-read; want still 1", got)
	}
}

// TestConnExtraEndpointsCurrentNilWhenWatcherOff documents the bypass
// when the env knob is unset (no watcher, no allocation, no extra
// endpoints injected into determineEndpoints).
func TestConnExtraEndpointsCurrentNilWhenWatcherOff(t *testing.T) {
	c := &Conn{}
	if got := c.extraEndpointsCurrent(); got != nil {
		t.Fatalf("watcher off: extraEndpointsCurrent must be nil; got %v", got)
	}
}

// TestExtraEndpointsParseAndApplyEnforcesSizeCapOnRead is a regression
// test for Codex P2 round 1 on PR #17: the size cap must be enforced
// during read (via io.LimitReader), not via a pre-read os.Stat. A pre-
// read stat creates a TOCTOU window: a concurrent writer could replace
// the file between stat and read, and the stale stat-based size check
// would fail to refuse a now-oversized file. The fix uses
// io.LimitReader on the open fd and asserts post-read length, closing
// the window.
//
// We can't easily simulate a TOCTOU race in a unit test, but we can
// assert the cap fires on a file that is actually oversized at read
// time. With the broken stat-based approach + a deliberately stale
// stat, this would have passed the stat check but failed the read
// check; with the fix it correctly fails.
func TestExtraEndpointsParseAndApplyEnforcesSizeCapOnRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extra.json")
	// Write exactly extraEndpointsMaxFileSize+100 bytes — well over cap.
	big := strings.Repeat("a", extraEndpointsMaxFileSize+100)
	if err := os.WriteFile(path, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestExtraEndpointsState(path, extraEndpointsDefaultMax)
	_, err := s.parseAndApply()
	if err == nil {
		t.Fatal("oversized file (cap+100 bytes) must be refused at read time")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected 'too large' error; got %v", err)
	}
}

// TestStopExtraEndpointsWatcherIdempotent is a regression test for Codex
// P1 round 1 on PR #17: concurrent stopExtraEndpointsWatcher calls (or
// repeated calls from a single goroutine) must not panic. The first
// implementation called close(stopCh) unconditionally — the second
// call would crash with "close of closed channel".
func TestStopExtraEndpointsWatcherIdempotent(t *testing.T) {
	c := &Conn{
		extraEndpoints: &extraEndpointsState{
			logf:   func(string, ...any) {},
			stopCh: make(chan struct{}),
		},
	}

	// First call — closes the channel.
	c.stopExtraEndpointsWatcher()

	// Second call — must NOT panic. (If sync.Once is missing, this
	// panics with "close of closed channel".)
	c.stopExtraEndpointsWatcher()

	// Third call — also fine.
	c.stopExtraEndpointsWatcher()

	// And concurrent calls — also fine.
	const N = 8
	done := make(chan struct{}, N)
	for i := 0; i < N; i++ {
		go func() {
			c.stopExtraEndpointsWatcher()
			done <- struct{}{}
		}()
	}
	for i := 0; i < N; i++ {
		<-done
	}
}
