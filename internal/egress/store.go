package egress

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// Store persists the allow/deny rules and the pending queue as controller-side
// JSON files (Popo's copy is authoritative, §3; never written by Nana).
type Store struct {
	approvalsPath string
	pendingPath   string
}

// NewStore opens a store over the two file paths.
func NewStore(approvalsPath, pendingPath string) *Store {
	return &Store{approvalsPath: approvalsPath, pendingPath: pendingPath}
}

func (s *Store) loadRules() rules {
	var r rules
	if data, err := os.ReadFile(s.approvalsPath); err == nil {
		_ = json.Unmarshal(data, &r)
	}
	return r
}

func (s *Store) saveRules(r rules) error {
	return writeJSON(s.approvalsPath, r)
}

// Allow / Deny return the current rule globs.
func (s *Store) Allow() []string { return s.loadRules().Allow }
func (s *Store) Deny() []string  { return s.loadRules().Deny }

// AddAllow persists an allow rule (idempotent).
func (s *Store) AddAllow(pattern string) error {
	r := s.loadRules()
	r.Allow = appendUnique(r.Allow, pattern)
	return s.saveRules(r)
}

// AddDeny persists a deny rule (idempotent). Deny is checked before allow.
func (s *Store) AddDeny(pattern string) error {
	r := s.loadRules()
	r.Deny = appendUnique(r.Deny, pattern)
	return s.saveRules(r)
}

// RemoveAllow forgets an allow rule (no-op if absent).
func (s *Store) RemoveAllow(pattern string) error {
	r := s.loadRules()
	r.Allow = removeStr(r.Allow, pattern)
	return s.saveRules(r)
}

// RemoveDeny forgets a deny rule (no-op if absent).
func (s *Store) RemoveDeny(pattern string) error {
	r := s.loadRules()
	r.Deny = removeStr(r.Deny, pattern)
	return s.saveRules(r)
}

// Pending returns the held entries.
func (s *Store) Pending() []PendingEntry {
	var p []PendingEntry
	if data, err := os.ReadFile(s.pendingPath); err == nil {
		_ = json.Unmarshal(data, &p)
	}
	return p
}

// AddPending records a held request, deduped by URL (re-submits of the same URL
// don't pile up).
func (s *Store) AddPending(e PendingEntry) error {
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	p := s.Pending()
	for _, x := range p {
		if x.URL == e.URL {
			return nil
		}
	}
	return writeJSON(s.pendingPath, append(p, e))
}

// RemovePending removes and returns the entry with id (ok=false if absent).
func (s *Store) RemovePending(id string) (PendingEntry, bool, error) {
	p := s.Pending()
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
	return found, true, writeJSON(s.pendingPath, out)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
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
