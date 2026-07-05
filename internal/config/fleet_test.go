package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFleet(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "iceclimber.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A classic single-sandbox file is a one-entry fleet — every fleet-aware command sees one view.
func TestFleet_ClassicSingleFileIsOneEntry(t *testing.T) {
	f, err := LoadFleet(writeFleet(t, "sandbox_id: solo\nssh:\n  host: h\n"))
	if err != nil {
		t.Fatal(err)
	}
	if ids := f.IDs(); len(ids) != 1 || ids[0] != "solo" {
		t.Fatalf("members = %v, want [solo]", ids)
	}
}

// The defaults template deep-merges into each member; the map key is the id; nested structs
// merge per field (an absent key inherits, a present key overrides — BOTH boolean directions).
func TestFleet_MergeAndOverrides(t *testing.T) {
	f, err := LoadFleet(writeFleet(t, `
defaults:
  ssh: { user: agent, keepalive_interval: 20, password_auth: true }
  egress_mode: relay
sandboxes:
  ci-1: { ssh: { host: 10.0.0.5 } }
  gpu:  { ssh: { host: gpu.corp, password_auth: false }, egress_mode: proxy }
`))
	if err != nil {
		t.Fatal(err)
	}
	if ids := strings.Join(f.IDs(), ","); ids != "ci-1,gpu" {
		t.Fatalf("ids = %q, want ci-1,gpu (sorted)", ids)
	}

	ci1, _ := f.Member("ci-1")
	if ci1.SandboxID != "ci-1" || ci1.SSH.Host != "10.0.0.5" {
		t.Fatalf("ci-1 id/host wrong: %+v", ci1.SSH)
	}
	if ci1.SSH.User != "agent" || ci1.SSH.KeepAliveInterval != 20 || !ci1.SSH.PasswordAuth || ci1.EgressMode != "relay" {
		t.Fatalf("ci-1 should inherit all defaults: %+v / egress=%s", ci1.SSH, ci1.EgressMode)
	}

	gpu, _ := f.Member("gpu")
	// nested field-wise merge: host + password_auth from the entry, user + keepalive inherited.
	if gpu.SSH.Host != "gpu.corp" || gpu.SSH.User != "agent" || gpu.SSH.KeepAliveInterval != 20 {
		t.Fatalf("gpu nested merge wrong: %+v", gpu.SSH)
	}
	if gpu.SSH.PasswordAuth {
		t.Fatal("entry `password_auth: false` MUST override the `true` default")
	}
	if gpu.EgressMode != "proxy" {
		t.Fatalf("entry egress_mode should override default: %q", gpu.EgressMode)
	}
}

// The other boolean direction: false (or absent) default, true entry.
func TestFleet_BooleanOverrideTrueOverFalse(t *testing.T) {
	f, err := LoadFleet(writeFleet(t, `
defaults: { ssh: { host: base } }
sandboxes:
  a: {}
  b: { ssh: { password_auth: true } }
`))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := f.Member("a")
	b, _ := f.Member("b")
	if a.SSH.PasswordAuth {
		t.Fatal("a should keep the default (false/absent)")
	}
	if !b.SSH.PasswordAuth {
		t.Fatal("b entry `password_auth: true` should override the absent/false default")
	}
	if a.SSH.Host != "base" || b.SSH.Host != "base" {
		t.Fatal("both should inherit the default host")
	}
}

func TestFleet_RejectsBothShapes(t *testing.T) {
	_, err := LoadFleet(writeFleet(t, "sandbox_id: x\nsandboxes:\n  a: { ssh: { host: h } }\n"))
	if err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("both sandbox_id + sandboxes must error, got %v", err)
	}
}

func TestFleet_PerMemberValidation(t *testing.T) {
	// gpu has no ssh.host anywhere → its member validation must fail, naming the member.
	_, err := LoadFleet(writeFleet(t, `
defaults: { ssh: { user: agent } }
sandboxes:
  ok:  { ssh: { host: h } }
  bad: {}
`))
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("a member missing ssh.host must fail validation naming it, got %v", err)
	}
}

func TestFleet_Resolve(t *testing.T) {
	f, err := LoadFleet(writeFleet(t, `
defaults: { ssh: { host: h } }
sandboxes:
  ci-1: {}
  ci-2: {}
  stage: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	ids := func(cs []*Config) string {
		var s []string
		for _, c := range cs {
			s = append(s, c.SandboxID)
		}
		return strings.Join(s, ",")
	}
	all, _ := f.Resolve("all")
	if ids(all) != "ci-1,ci-2,stage" {
		t.Fatalf("all = %q", ids(all))
	}
	glob, _ := f.Resolve("ci-*")
	if ids(glob) != "ci-1,ci-2" {
		t.Fatalf("glob ci-* = %q", ids(glob))
	}
	list, _ := f.Resolve("stage,ci-1")
	if ids(list) != "ci-1,stage" { // sorted + deduped
		t.Fatalf("list = %q, want sorted ci-1,stage", ids(list))
	}
	dedup, _ := f.Resolve("ci-*,ci-1")
	if ids(dedup) != "ci-1,ci-2" {
		t.Fatalf("dedup = %q", ids(dedup))
	}
	if _, err := f.Resolve("nope"); err == nil {
		t.Fatal("a selector matching nothing must error")
	}
	if _, err := f.Resolve("ci-*,ghost"); err == nil {
		t.Fatal("a list with an unmatched part must error")
	}
}

// single-box Load against a fleet file: names one member; errors (ambiguous) with no selector.
func TestFleet_SingleBoxLoadAgainstFleet(t *testing.T) {
	body := "defaults: { ssh: { host: h } }\nsandboxes:\n  a: {}\n  b: {}\n"
	p := writeFleet(t, body)
	// explicit --config path with a selector naming one member
	c, err := Load(p, "b")
	if err != nil || c.SandboxID != "b" {
		t.Fatalf("Load fleet with --sandbox b = %+v, %v", c, err)
	}
	if _, err := Load(p, ""); err == nil || !strings.Contains(err.Error(), "fleet") {
		t.Fatalf("Load fleet with no selector must ask for one, got %v", err)
	}
	if _, err := Load(p, "ghost"); err == nil {
		t.Fatal("Load fleet with an unknown member must error")
	}
}
