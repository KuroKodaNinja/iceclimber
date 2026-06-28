//go:build scenario

// Package harness provides the shared Lima-sandbox plumbing for the self-contained
// per-language application scenarios under test/scenarios/. It mirrors the
// functional suite's connection helpers (discover the VM, build the binary, write a
// config, run the binary, shell into the VM, dial a RemoteFS) so each scenario
// directory only needs its app, its test, and a README.
//
// Gated by the `scenario` build tag, so plain `go test ./...`, `make test`, and
// `make test-functional` never compile it. Run the scenarios with `make scenario`.
package harness
