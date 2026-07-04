package config

import (
	"strings"
	"testing"
)

// TestLookupRuntimePin covers the resolver hook: a covered platform returns the asset +
// full version; the full version defaults to the requested key when unset; and every
// miss axis (lang / version / platform) falls through with ok=false.
func TestLookupRuntimePin(t *testing.T) {
	pins := map[string]map[string]RuntimePin{
		"python": {
			"3.12": {
				Full: "3.12.13",
				Platforms: map[string]RuntimePinAsset{
					"linux/aarch64/musl": {URL: "https://mirror/py-musl.tar.gz", SHA256: "aaaa"},
					"linux/x86_64/glibc": {URL: "https://mirror/py-gnu.tar.gz", SHA256: "bbbb"},
				},
			},
			"3.13": { // no Full: defaults to the requested key
				Platforms: map[string]RuntimePinAsset{
					"linux/aarch64/musl": {URL: "https://mirror/py313-musl.tar.gz", SHA256: "cccc"},
				},
			},
		},
	}

	url, sha, full, ok := LookupRuntimePin(pins, "python", "3.12", "linux", "aarch64", "musl")
	if !ok || url != "https://mirror/py-musl.tar.gz" || sha != "aaaa" || full != "3.12.13" {
		t.Errorf("hit = (%q,%q,%q,%v), want the musl asset with full 3.12.13", url, sha, full, ok)
	}

	// Full defaults to the requested version when the pin omits it.
	if _, _, full, ok := LookupRuntimePin(pins, "python", "3.13", "linux", "aarch64", "musl"); !ok || full != "3.13" {
		t.Errorf("full-default = (%q,%v), want (\"3.13\", true)", full, ok)
	}

	// Miss axes: unknown lang, unknown version, uncovered platform.
	for _, m := range []struct {
		name                      string
		lang, ver, os, arch, libc string
	}{
		{"lang", "node", "3.12", "linux", "aarch64", "musl"},
		{"version", "python", "3.11", "linux", "aarch64", "musl"},
		{"platform", "python", "3.12", "linux", "aarch64", "glibc"},
	} {
		if _, _, _, ok := LookupRuntimePin(pins, m.lang, m.ver, m.os, m.arch, m.libc); ok {
			t.Errorf("%s miss: ok=true, want false", m.name)
		}
	}
}

// TestLoad_RuntimePins_Valid loads a real runtime_pins block end to end (parse + validate)
// and resolves it, proving the config surface is wired.
func TestLoad_RuntimePins_Valid(t *testing.T) {
	path := writeTemp(t, `
sandbox_id: box-1
ssh:
  host: example.internal
runtime_pins:
  python:
    "3.12":
      full: 3.12.13
      platforms:
        linux/aarch64/musl:
          url: https://mirror.internal/cpython-3.12.13-musl.tar.gz
          sha256: deadbeef
`)
	cfg, err := Load(path, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	url, sha, full, ok := LookupRuntimePin(cfg.RuntimePins, "python", "3.12", "linux", "aarch64", "musl")
	if !ok || full != "3.12.13" || sha != "deadbeef" || !strings.HasSuffix(url, "musl.tar.gz") {
		t.Errorf("loaded pin = (%q,%q,%q,%v), unexpected", url, sha, full, ok)
	}
}

// TestLoad_RuntimePins_Invalid rejects unverifiable / malformed pins at load — the air-gap
// supply-chain floor (a pin with no checksum, or a non os/arch/libc platform key).
func TestLoad_RuntimePins_Invalid(t *testing.T) {
	cases := map[string]string{
		"missing sha256": `
sandbox_id: box-1
ssh: {host: example.internal}
runtime_pins:
  python:
    "3.12":
      platforms:
        linux/aarch64/musl:
          url: https://mirror/py.tar.gz
`,
		"missing url": `
sandbox_id: box-1
ssh: {host: example.internal}
runtime_pins:
  python:
    "3.12":
      platforms:
        linux/aarch64/musl:
          sha256: abc
`,
		"bad platform key": `
sandbox_id: box-1
ssh: {host: example.internal}
runtime_pins:
  python:
    "3.12":
      platforms:
        linux-aarch64:
          url: https://mirror/py.tar.gz
          sha256: abc
`,
	}
	for name, body := range cases {
		if _, err := Load(writeTemp(t, body), ""); err == nil {
			t.Errorf("%s: Load succeeded, want a validation error", name)
		}
	}
}
