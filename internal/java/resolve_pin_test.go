package java

import (
	"context"
	"net/http"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/relay"
)

// failTransport fails the test if any HTTP request is issued — the proof that a pin
// short-circuits the resolver before it touches the network.
type failTransport struct{ t *testing.T }

func (f failTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Fatalf("resolve made a network call on a pin hit: %s %s", r.Method, r.URL)
	return nil, nil
}

// rtFunc adapts a function to http.RoundTripper.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestResolvePinShortCircuits proves an operator pin returns the exact asset without any upstream
// metadata call (the air-gapped-mirror path, §17): the HTTP client fails on use, yet resolve()
// succeeds from the pin alone.
func TestResolvePinShortCircuits(t *testing.T) {
	pin := relay.Pin{
		FullVersion: "21.0.4",
		AssetName:   "pin-jdk-21.0.4-musl.tar.gz",
		URL:         "https://mirror.internal/jdk-21.0.4-musl.tar.gz",
		SHA256:      "abc123",
	}
	i := NewInstaller(Config{
		OS:         "linux",
		Arch:       "aarch64",
		Libc:       "musl",
		HTTPClient: &http.Client{Transport: failTransport{t}},
		Pin: func(version string) (relay.Pin, bool) {
			if version != "21" {
				t.Errorf("Pin called with %q, want the requested feature 21", version)
			}
			return pin, true
		},
	})

	r, err := i.resolve(context.Background(), "21")
	if err != nil {
		t.Fatalf("resolve on a pin hit: %v", err)
	}
	if r.FullVersion != pin.FullVersion || r.AssetName != pin.AssetName || r.URL != pin.URL || r.SHA256 != pin.SHA256 {
		t.Errorf("resolved = %+v, want the pin %+v", r, pin)
	}
}

// TestResolvePinMissFallsThrough proves a Pin returning false does NOT short-circuit — resolve()
// proceeds to the live path (which then hits the booby-trapped client).
func TestResolvePinMissFallsThrough(t *testing.T) {
	called := false
	i := NewInstaller(Config{
		OS:   "linux",
		Arch: "aarch64",
		Libc: "musl",
		HTTPClient: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			called = true
			return nil, context.Canceled
		})},
		Pin: func(string) (relay.Pin, bool) { return relay.Pin{}, false },
	})

	if _, err := i.resolve(context.Background(), "21"); err == nil {
		t.Fatal("expected the live resolve to run (and fail on the stub transport)")
	}
	if !called {
		t.Error("resolve did not fall through to the live path on a pin miss")
	}
}
