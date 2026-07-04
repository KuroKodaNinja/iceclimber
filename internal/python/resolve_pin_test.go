package python

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

// TestResolvePinShortCircuits proves an operator pin returns the exact asset without any
// upstream metadata call (the air-gapped-mirror path, §17): the HTTP client is booby-trapped
// to fail on use, yet resolve() succeeds from the pin alone.
func TestResolvePinShortCircuits(t *testing.T) {
	pin := relay.Pin{
		FullVersion: "3.12.13",
		AssetName:   "pin-cpython-3.12.13-musl.tar.gz",
		URL:         "https://mirror.internal/cpython-3.12.13-musl.tar.gz",
		SHA256:      "abc123",
	}
	i := NewInstaller(Config{
		OS:         "linux",
		Arch:       "aarch64",
		Libc:       "musl",
		HTTPClient: &http.Client{Transport: failTransport{t}},
		Pin: func(version string) (relay.Pin, bool) {
			if version != "3.12" {
				t.Errorf("Pin called with %q, want the requested minor 3.12", version)
			}
			return pin, true
		},
	})

	r, err := i.resolve(context.Background(), "3.12")
	if err != nil {
		t.Fatalf("resolve on a pin hit: %v", err)
	}
	if r.FullVersion != pin.FullVersion || r.AssetName != pin.AssetName || r.URL != pin.URL || r.SHA256 != pin.SHA256 {
		t.Errorf("resolved = %+v, want the pin %+v", r, pin)
	}
}

// TestResolvePinMissFallsThrough proves a Pin that returns false does NOT short-circuit —
// resolve() proceeds to the live path (which then hits the booby-trapped client, our signal
// that the fall-through happened rather than the pin swallowing the miss).
func TestResolvePinMissFallsThrough(t *testing.T) {
	called := false
	i := NewInstaller(Config{
		OS:   "linux",
		Arch: "aarch64",
		Libc: "musl",
		HTTPClient: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			called = true
			return nil, context.Canceled // abort the live resolve cheaply
		})},
		Pin: func(string) (relay.Pin, bool) { return relay.Pin{}, false },
	})

	if _, err := i.resolve(context.Background(), "3.12"); err == nil {
		t.Fatal("expected the live resolve to run (and fail on the stub transport)")
	}
	if !called {
		t.Error("resolve did not fall through to the live path on a pin miss")
	}
}

// rtFunc adapts a function to http.RoundTripper.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
