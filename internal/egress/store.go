package egress

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// rules is the operator-owned allow/deny rule set (approvals.json).
type rules struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

// PendingEntry is a controller-venue fetch held for approval.
type PendingEntry struct {
	ID   string `json:"id"`
	URL  string `json:"url"`
	Host string `json:"host"`
	TS   string `json:"ts"`
}

// Store persists the allow/deny rules and the pending queue as controller-side JSON files
// (Popo's copy is authoritative, §3; never written by Nana). It is safe under concurrent
// goroutines AND processes: the `serve`/console handler mutates it from parallel dispatch
// (decision #84) while a separate `iceclimber approve/deny` process writes the same files.
// Each read-modify-write is serialized by an in-process mutex plus an advisory flock on the
// target file, and every write is atomic (temp + rename) — so two writers never clobber and a
// reader never sees a half-written file (the same pattern internal/relay uses for its cache).
type Store struct {
	approvalsPath string
	pendingPath   string
	mu            sync.Mutex
}

// NewStore opens a store over the two file paths.
func NewStore(approvalsPath, pendingPath string) *Store {
	return &Store{approvalsPath: approvalsPath, pendingPath: pendingPath}
}

// load reads the rules. An absent file is empty rules (nil error); a present-but-unparseable
// file is an ERROR, never silently empty — a corrupt approvals file must fail closed (the gate
// treats the error as "deny"), and a mutation must abort rather than overwrite rules it could
// not read (which would silently wipe the operator's allow/deny set).
func (s *Store) load() (rules, error) {
	var r rules
	data, err := os.ReadFile(s.approvalsPath)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if len(data) == 0 {
		return r, nil
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("corrupt approvals file %s: %w", s.approvalsPath, err)
	}
	return r, nil
}

func (s *Store) loadPending() ([]PendingEntry, error) {
	var p []PendingEntry
	data, err := os.ReadFile(s.pendingPath)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if len(data) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("corrupt pending file %s: %w", s.pendingPath, err)
	}
	return p, nil
}

// Allow / Deny return the current rule globs (best-effort for display/CLI; the gate uses the
// error-aware load() directly so it can fail closed on a corrupt file).
func (s *Store) Allow() []string { r, _ := s.load(); return r.Allow }
func (s *Store) Deny() []string  { r, _ := s.load(); return r.Deny }

// AddAllow persists an allow rule (idempotent). The pattern is canonicalized (so it matches
// canonical request URLs) and validated as a full-URL glob so a malformed rule fails loudly
// instead of silently never matching.
func (s *Store) AddAllow(pattern string) error {
	return s.mutate(func(r *rules) error {
		p := Canonical(pattern)
		if err := ValidateURLGlob(p); err != nil {
			return fmt.Errorf("allow %w", err)
		}
		r.Allow = appendUnique(r.Allow, p)
		return nil
	})
}

// AddDeny persists a deny rule (idempotent). Deny is checked before allow. Canonicalized +
// validated as a full-URL glob (see AddAllow).
func (s *Store) AddDeny(pattern string) error {
	return s.mutate(func(r *rules) error {
		p := Canonical(pattern)
		if err := ValidateURLGlob(p); err != nil {
			return fmt.Errorf("deny %w", err)
		}
		r.Deny = appendUnique(r.Deny, p)
		return nil
	})
}

// RemoveAllow forgets an allow rule (no-op if absent). Matches the canonical form AddAllow stored.
func (s *Store) RemoveAllow(pattern string) error {
	return s.mutate(func(r *rules) error { r.Allow = removeStr(r.Allow, Canonical(pattern)); return nil })
}

// RemoveDeny forgets a deny rule (no-op if absent).
func (s *Store) RemoveDeny(pattern string) error {
	return s.mutate(func(r *rules) error { r.Deny = removeStr(r.Deny, Canonical(pattern)); return nil })
}

// mutate runs a read-modify-write on the rules file under the in-process mutex AND a cross-
// process flock, aborting (without writing) if the current file can't be parsed — so a
// concurrent writer can't clobber and a corrupt file can't be silently reset to empty.
func (s *Store) mutate(fn func(*rules) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := flockFile(s.approvalsPath)
	if err != nil {
		return err
	}
	defer unlock()
	r, err := s.load()
	if err != nil {
		return err
	}
	if err := fn(&r); err != nil {
		return err
	}
	return writeJSONAtomic(s.approvalsPath, r)
}

// Pending returns the held entries (best-effort; a corrupt file yields none).
func (s *Store) Pending() []PendingEntry {
	p, _ := s.loadPending()
	return p
}

// AddPending records a held request, deduped by URL (re-submits of the same URL don't pile
// up). Serialized like the rule mutations so parallel dispatch can't drop a held request.
func (s *Store) AddPending(e PendingEntry) error {
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := flockFile(s.pendingPath)
	if err != nil {
		return err
	}
	defer unlock()
	p, err := s.loadPending()
	if err != nil {
		return err
	}
	for _, x := range p {
		if x.URL == e.URL {
			return nil
		}
	}
	return writeJSONAtomic(s.pendingPath, append(p, e))
}

// RemovePending removes and returns the entry with id (ok=false if absent).
func (s *Store) RemovePending(id string) (PendingEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := flockFile(s.pendingPath)
	if err != nil {
		return PendingEntry{}, false, err
	}
	defer unlock()
	p, err := s.loadPending()
	if err != nil {
		return PendingEntry{}, false, err
	}
	out := p[:0:0]
	var found PendingEntry
	ok := false
	for _, x := range p {
		if x.ID == id {
			found, ok = x, true
			continue
		}
		out = append(out, x)
	}
	if !ok {
		return PendingEntry{}, false, nil
	}
	return found, true, writeJSONAtomic(s.pendingPath, out)
}

// writeJSONAtomic writes v as pretty JSON via a temp file + rename, so a concurrent reader
// sees either the old or the new complete file, never a truncated one.
func writeJSONAtomic(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// flockFile takes an exclusive advisory lock on a sidecar "<path>.lock" (blocking), returning
// an unlock func. The sidecar avoids locking the data file itself, which the atomic rename
// replaces out from under an fd.
func flockFile(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func removeStr(xs []string, x string) []string {
	out := xs[:0:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

func appendUnique(xs []string, x string) []string {
	for _, e := range xs {
		if e == x {
			return xs
		}
	}
	return append(xs, x)
}
