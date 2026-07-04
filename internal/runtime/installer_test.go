package runtime

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/relay"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

// failTransport fails the test if any HTTP request is issued — the proof that a pin
// short-circuits the resolver before it touches the network.
type failTransport struct{ t *testing.T }

func (f failTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Fatalf("resolve made a network call on a pin hit: %s %s", r.Method, r.URL)
	return nil, nil
}

// TestResolvePinShortCircuits proves an operator pin returns the exact asset without ever
// invoking the language's live resolver (the air-gapped-mirror path, §17): the Spec's Resolve
// fails the test if reached, and the HTTP client is booby-trapped, yet resolve() succeeds from
// the pin alone. This is the single engine-level test of the short-circuit the per-language
// installers used to each carry.
func TestResolvePinShortCircuits(t *testing.T) {
	pin := relay.Pin{
		FullVersion: "1.2.3",
		AssetName:   "pin-x-1.2.3.tar.gz",
		URL:         "https://mirror.internal/x-1.2.3.tar.gz",
		SHA256:      "abc123",
	}
	spec := Spec{
		Lang:    "x",
		BinName: "x",
		Resolve: func(context.Context, Fingerprint, *http.Client, string) (relay.Pin, error) {
			t.Fatal("live resolver called on a pin hit")
			return relay.Pin{}, nil
		},
	}
	i := New(spec, Config{
		OS:         "linux",
		Arch:       "aarch64",
		Libc:       "musl",
		HTTPClient: &http.Client{Transport: failTransport{t}},
		Pin: func(version string) (relay.Pin, bool) {
			if version != "x" {
				t.Errorf("Pin called with %q, want the requested version x", version)
			}
			return pin, true
		},
	})

	r, err := i.resolve(context.Background(), "x")
	if err != nil {
		t.Fatalf("resolve on a pin hit: %v", err)
	}
	if r != pin {
		t.Errorf("resolved = %+v, want the pin %+v", r, pin)
	}
}

// TestResolvePinMissFallsThrough proves a Pin returning false does NOT short-circuit — resolve()
// proceeds to the Spec's live resolver (our signal that the fall-through happened rather than the
// pin swallowing the miss).
func TestResolvePinMissFallsThrough(t *testing.T) {
	called := false
	spec := Spec{
		Lang:    "x",
		BinName: "x",
		Resolve: func(_ context.Context, _ Fingerprint, _ *http.Client, version string) (relay.Pin, error) {
			called = true
			if version != "x" {
				t.Errorf("Resolve called with %q, want the requested version x", version)
			}
			return relay.Pin{}, context.Canceled // abort the live resolve cheaply
		},
	}
	i := New(spec, Config{
		OS:   "linux",
		Arch: "aarch64",
		Libc: "musl",
		Pin:  func(string) (relay.Pin, bool) { return relay.Pin{}, false },
	})

	if _, err := i.resolve(context.Background(), "x"); err == nil {
		t.Fatal("expected the live resolver to run (and fail)")
	}
	if !called {
		t.Error("resolve did not fall through to the live resolver on a pin miss")
	}
}

// TestHandlerMalformedParams proves the shared handler rejects unparseable params with the
// malformed_params code before it ever touches the installer (which is why a Spec with a
// test-failing Resolve is safe here — it's never reached).
func TestHandlerMalformedParams(t *testing.T) {
	inst := New(Spec{
		Lang:    "x",
		BinName: "x",
		Resolve: func(context.Context, Fingerprint, *http.Client, string) (relay.Pin, error) {
			t.Fatal("installer reached on malformed params")
			return relay.Pin{}, nil
		},
	}, Config{})
	h := Handler(inst)
	r := h(context.Background(), protocol.Request{ID: "1", Params: json.RawMessage("{")})
	if r.Error == nil || r.Error.Code != "malformed_params" {
		t.Errorf("malformed params: got %+v", r.Error)
	}
}

// TestExtractAndPush_ReportsTransferProgress proves the shared push reports byte progress against
// the compressed tarball size and flushes a final full-count event — over a real (local) ExecFS
// extraction (the coverage the per-language push tests used to carry, now tested once).
func TestExtractAndPush_ReportsTransferProgress(t *testing.T) {
	dir := t.TempDir()
	tarball := makeTarGz(t, dir)
	size, _ := os.Stat(tarball)

	var events []progress.Event
	inst := &Installer{cfg: Config{
		FS:       remotefs.NewExecFS(remotefstest.LocalRunner{}),
		Progress: func(e progress.Event) { events = append(events, e) },
	}}
	target := filepath.Join(dir, "out")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := inst.extractAndPush(context.Background(), tarball, target); err != nil {
		t.Fatalf("extractAndPush: %v", err)
	}

	if len(events) == 0 {
		t.Fatal("no progress events emitted during transfer")
	}
	last := events[len(events)-1]
	if last.Phase != "transferring" || last.Unit != progress.Bytes {
		t.Errorf("event = %+v, want a transferring/Bytes event", last)
	}
	if last.Total != size.Size() || last.Cur != size.Size() {
		t.Errorf("final event = %d/%d, want full compressed size %d", last.Cur, last.Total, size.Size())
	}
	// The top-level component was stripped and the file extracted.
	if _, err := os.Stat(filepath.Join(target, "bin", "x")); err != nil {
		t.Errorf("expected extracted file: %v", err)
	}
}

// makeTarGz writes a minimal gzip'd tar with a single top-level dir (which the push strips)
// containing one file, and returns its path.
func makeTarGz(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "runtime.tar.gz")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	body := []byte("#!/bin/sh\necho ok\n")
	for _, h := range []*tar.Header{
		{Name: "top/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "top/bin/x", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
	tw.Close()
	gw.Close()
	return p
}
