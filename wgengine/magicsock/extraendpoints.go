// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package magicsock

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"tailscale.com/envknob"
)

// Phase 21 — dynamic multi-endpoint advertise.
//
// magicsock today gathers endpoints in determineEndpoints() from a fixed
// set of intrinsic sources: STUN, NAT-PMP/PCP/UPnP port-mapping, locally-
// bound interface addresses, and a small static-config slot
// (Options.StaticEndpoints, set once at startup). Operators who DNAT
// multiple public IP:port front doors to the same tailscaled — e.g. a
// load-balancer / "rotating IPs against censorship" controller —
// currently have no way to tell tailscaled about those alternate front
// doors at runtime. STUN sees only the one mapping its outbound used,
// port-mapping protocols target the local NAT, and StaticEndpoints is
// restart-only.
//
// When TS_EXPERIMENTAL_EXTRA_ENDPOINTS_FILE points at a JSON file like
//
//	{"endpoints": ["P1:41641", "P2:41641", "P3:41641"]}
//
// magicsock now watches that file and includes its endpoints alongside
// the intrinsic set in every determineEndpoints() cycle. File changes
// trigger an immediate ReSTUN("extra-endpoints-changed"), which propagates
// to peers via the existing Hostinfo / map request flow within ~1 s. With
// the env knob unset, the feature is fully off and behaviour is bit-
// identical to before.
//
// Threat model: WireGuard handshake at the destination authenticates the
// data plane regardless of how the client learned the IP:port, so a peer
// publishing fictional endpoints can route garbage but cannot impersonate.
// The new local trust surface is the file's filesystem permissions: any
// process that can write the file publishes endpoints under this node's
// identity. On unix-like systems the watcher refuses to read files that
// are group-writable or world-writable. Operators wanting tighter control
// can keep the file at 0600 owner=root.

var (
	envknobExtraEndpointsFile  = envknob.RegisterString("TS_EXPERIMENTAL_EXTRA_ENDPOINTS_FILE")
	envknobExtraEndpointsMax   = envknob.RegisterInt("TS_EXPERIMENTAL_EXTRA_ENDPOINTS_MAX")
	envknobExtraEndpointsPollS = envknob.RegisterInt("TS_EXPERIMENTAL_EXTRA_ENDPOINTS_POLL_S")
)

const (
	// extraEndpointsDefaultMax is the default policy cap on how many
	// extra endpoints from the file are honored per parse cycle.
	//
	// 0 means *unlimited*: the watcher honors every entry the file lists
	// (subject only to the file-size memory-safety cap). This matches the
	// project's permissive-caps stance — operators with hundreds or
	// thousands of front-door entries don't have to remember to widen a
	// policy knob, and small deployments pay nothing extra. Operators who
	// want a hard policy cap (e.g. defense-in-depth against a misconfigured
	// upstream that stuffs the file with junk) can set
	// TS_EXPERIMENTAL_EXTRA_ENDPOINTS_MAX explicitly.
	extraEndpointsDefaultMax = 0

	// extraEndpointsMaxFileSize is the memory-safety ceiling on file size
	// to read (the parser refuses anything larger). Sized to hold a
	// 100 000-entry baseline deployment with ~10× headroom for both
	// formatting overhead and growth:
	//
	//   "[2001:0db8:0000:0000:0000:0000:0000:0001]:65535",
	//
	// is ~50–60 bytes including the trailing comma + newline; 100k of
	// these is ~6 MB. 64 MB / 60 ≈ 1.1 M entries — far above any
	// plausible deployment, while still bounding memory against a
	// runaway / corrupt file (e.g. an upstream that accidentally
	// concatenates a system log into the endpoints file). This is a
	// pure memory-safety guard, not a policy constraint — the policy
	// cap lives in TS_EXPERIMENTAL_EXTRA_ENDPOINTS_MAX (default 0 =
	// unlimited).
	extraEndpointsMaxFileSize = 64 * 1024 * 1024
)

// extraEndpointsFile is the JSON shape parsed from
// TS_EXPERIMENTAL_EXTRA_ENDPOINTS_FILE. Unknown fields are ignored to
// allow forward-compatible extension.
type extraEndpointsFile struct {
	Endpoints []string `json:"endpoints"`
}

// extraEndpointsState holds runtime state for Phase 21. One instance per
// Conn; nil if the env knob is unset (feature off).
type extraEndpointsState struct {
	logf func(format string, args ...any)
	path string
	max  int

	// reload is invoked after a successful re-parse with a non-empty
	// delta against the previously-cached set, so the magicsock layer can
	// trigger ReSTUN on change. May be nil in tests.
	reload func()

	mu  sync.RWMutex
	cur []netip.AddrPort // last successfully-parsed endpoints

	stopCh   chan struct{}
	stopOnce sync.Once // guards close(stopCh) against concurrent stopExtraEndpointsWatcher calls
	wg       sync.WaitGroup
}

// extraEndpointsCurrent returns the current snapshot of operator-provided
// extra endpoints. Thread-safe. Returns nil if the feature is off or the
// file has no valid endpoints to advertise.
//
// Caller must NOT hold c.mu.
func (c *Conn) extraEndpointsCurrent() []netip.AddrPort {
	s := c.extraEndpoints
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.cur) == 0 {
		return nil
	}
	out := make([]netip.AddrPort, len(s.cur))
	copy(out, s.cur)
	return out
}

// startExtraEndpointsWatcher initializes Phase 21 if
// TS_EXPERIMENTAL_EXTRA_ENDPOINTS_FILE is set and starts the watcher
// goroutine. Returns nil even on failure (e.g. invalid initial file
// contents) — the watcher will keep running and pick up corrected
// content on the next file change. A startup error is logged but does
// not block magicsock initialization.
//
// Caller must NOT hold c.mu.
func (c *Conn) startExtraEndpointsWatcher() {
	path := envknobExtraEndpointsFile()
	if path == "" {
		return
	}

	s := &extraEndpointsState{
		logf:   c.logf,
		path:   path,
		max:    extraEndpointsMaxFromEnv(),
		reload: func() { c.ReSTUN("extra-endpoints-changed") },
		stopCh: make(chan struct{}),
	}

	// Try an initial read so the first determineEndpoints() cycle can
	// pick up the existing file contents without waiting for fsnotify.
	if eps, err := s.parseAndApply(); err != nil {
		s.logf("magicsock: extra-endpoints: initial read of %q failed: %v (watcher continues)", s.path, err)
	} else if len(eps) > 0 {
		s.logf("magicsock: extra-endpoints: loaded %d endpoint(s) from %q", len(eps), s.path)
	}

	s.wg.Add(1)
	go s.runWatcher(c.connCtx)
	c.extraEndpoints = s
}

// stopExtraEndpointsWatcher tears down the watcher and waits for the
// goroutine to exit. Safe to call concurrently / multiple times — the
// stopCh close is guarded by sync.Once so two simultaneous Close calls
// cannot panic with "close of closed channel".
func (c *Conn) stopExtraEndpointsWatcher() {
	s := c.extraEndpoints
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
	// Note: c.extraEndpoints is intentionally NOT zeroed here, since
	// other concurrent Close calls may already be observing it. The
	// closed-and-drained state machine is idempotent: subsequent calls
	// re-enter stopOnce.Do as a no-op and wg.Wait returns immediately.
}

// extraEndpointsMaxFromEnv returns the operator-configured per-cycle
// endpoint cap, or 0 (= unlimited) when the env knob is unset or
// non-positive. The parser interprets a returned value of 0 as "no
// policy cap; honor every parsed entry below the file-size ceiling".
func extraEndpointsMaxFromEnv() int {
	n := envknobExtraEndpointsMax()
	if n <= 0 {
		return extraEndpointsDefaultMax
	}
	return n
}

// extraEndpointsPollDuration returns the operator-configured polling
// interval, or 0 to disable polling (fsnotify-only). v1 default = 0.
func extraEndpointsPollDuration() time.Duration {
	n := envknobExtraEndpointsPollS()
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// runWatcher is the watcher goroutine. Owns its own fsnotify.Watcher and
// (optionally) a polling ticker. Re-parses the file on each event and
// invokes s.reload() when the parsed set changes.
func (s *extraEndpointsState) runWatcher(ctx context.Context) {
	defer s.wg.Done()

	w, werr := fsnotify.NewWatcher()
	if werr != nil {
		s.logf("magicsock: extra-endpoints: fsnotify.NewWatcher: %v (watcher disabled, periodic poll only if configured)", werr)
	} else {
		defer w.Close()
		// Watch the file's parent directory rather than the file itself so
		// rename-replace edits (the common atomic-write idiom) are caught.
		dir := filepath.Dir(s.path)
		if err := w.Add(dir); err != nil {
			s.logf("magicsock: extra-endpoints: fsnotify add %q: %v (watcher disabled, periodic poll only if configured)", dir, err)
			w.Close()
			w = nil
		}
	}

	var pollC <-chan time.Time
	if d := extraEndpointsPollDuration(); d > 0 {
		t := time.NewTicker(d)
		defer t.Stop()
		pollC = t.C
	}

	for {
		// fsnotify channels are nil when w is nil — receive from a nil
		// channel blocks forever, so the select naturally degrades to
		// "stop / poll only".
		var fsEvents <-chan fsnotify.Event
		var fsErrors <-chan error
		if w != nil {
			fsEvents = w.Events
			fsErrors = w.Errors
		}
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case ev, ok := <-fsEvents:
			if !ok {
				return
			}
			// Only react to changes touching our exact file. Other files in
			// the directory are noise.
			if ev.Name != "" && filepath.Clean(ev.Name) != filepath.Clean(s.path) {
				continue
			}
			s.handleFileChange()
		case err, ok := <-fsErrors:
			if !ok {
				return
			}
			s.logf("magicsock: extra-endpoints: fsnotify error: %v", err)
		case <-pollC:
			s.handleFileChange()
		}
	}
}

// handleFileChange re-parses the file and triggers reload if the parsed
// set differs from the previous snapshot.
func (s *extraEndpointsState) handleFileChange() {
	prev := s.snapshot()
	eps, err := s.parseAndApply()
	if err != nil {
		s.logf("magicsock: extra-endpoints: re-read failed: %v", err)
		return
	}
	if !addrPortSlicesEqual(prev, eps) {
		metricExtraEndpointsReloads.Add(1)
		if s.reload != nil {
			s.reload()
		}
	}
}

// snapshot returns a copy of the current cached endpoint set. Cheap
// helper to avoid leaking the unprotected slice.
func (s *extraEndpointsState) snapshot() []netip.AddrPort {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.cur) == 0 {
		return nil
	}
	out := make([]netip.AddrPort, len(s.cur))
	copy(out, s.cur)
	return out
}

// parseAndApply reads s.path, validates and parses it, and on success
// atomically swaps s.cur to the new slice. Returns the new slice (or
// nil when the file is missing — that is treated as "no extra
// endpoints" rather than as an error).
func (s *extraEndpointsState) parseAndApply() ([]netip.AddrPort, error) {
	// Open first, then check permissions and size *on the open fd* so
	// that a writer who replaces or grows the file between stat and read
	// cannot bypass the size cap (TOCTOU). os.OpenFile on a missing
	// file returns an error we handle as "feature off" rather than as
	// a hard failure.
	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.applyLocked(nil)
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("path is a directory, not a regular file")
	}
	if err := s.checkPermissions(info); err != nil {
		return nil, err
	}

	// Read up to extraEndpointsMaxFileSize+1 bytes via LimitReader. If
	// we got more than the cap we know the actual file exceeded it
	// (race-free against post-stat growth), so refuse. The +1 is what
	// distinguishes "exactly at cap" from "over cap".
	data, err := io.ReadAll(io.LimitReader(f, extraEndpointsMaxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > extraEndpointsMaxFileSize {
		return nil, errors.New("file too large; refusing to read")
	}
	var fc extraEndpointsFile
	if err := json.Unmarshal(data, &fc); err != nil {
		return nil, err
	}

	parsed := make([]netip.AddrPort, 0, len(fc.Endpoints))
	seen := make(map[netip.AddrPort]struct{}, len(fc.Endpoints))
	dropped := 0
	for _, raw := range fc.Endpoints {
		ap, perr := netip.ParseAddrPort(raw)
		if perr != nil {
			dropped++
			continue
		}
		if !ap.IsValid() {
			dropped++
			continue
		}
		if _, dup := seen[ap]; dup {
			dropped++
			continue
		}
		if s.max > 0 && len(parsed) >= s.max {
			// Policy cap from TS_EXPERIMENTAL_EXTRA_ENDPOINTS_MAX. Skipped
			// entirely when the env is unset (s.max == 0 = unlimited).
			dropped++
			continue
		}
		seen[ap] = struct{}{}
		parsed = append(parsed, ap)
	}
	if dropped > 0 {
		s.logf("magicsock: extra-endpoints: dropped %d invalid/duplicate/over-cap entries", dropped)
	}
	s.applyLocked(parsed)
	metricExtraEndpointsReads.Add(1)
	return parsed, nil
}

// applyLocked atomically swaps the cached snapshot.
func (s *extraEndpointsState) applyLocked(eps []netip.AddrPort) {
	s.mu.Lock()
	s.cur = eps
	s.mu.Unlock()
}

// checkPermissions refuses to read group-writable or world-writable
// files on unix-like systems. On Windows the synthesized mode bits do
// not have meaningful group/world semantics, so we skip the check
// there.
func (s *extraEndpointsState) checkPermissions(info os.FileInfo) error {
	if runtime.GOOS == "windows" || runtime.GOOS == "js" {
		return nil
	}
	mode := info.Mode().Perm()
	if mode&0o022 != 0 {
		return errors.New("file is group-writable or world-writable; refusing to read (set permissions to 0600 or 0644)")
	}
	return nil
}

// addrPortSlicesEqual compares two slices for set-equality (order-
// insensitive). Returns true iff both contain exactly the same elements.
func addrPortSlicesEqual(a, b []netip.AddrPort) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	seen := make(map[netip.AddrPort]int, len(a))
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
		if seen[x] < 0 {
			return false
		}
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}
