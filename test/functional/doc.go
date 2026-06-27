// Package functional holds black-box end-to-end tests that drive the real
// iceclimber binary against a Lima/Alpine sandbox VM. The tests are gated
// behind the "functional" build tag (see the Makefile's test-functional
// target); plain `go test ./...` compiles only this file and reports no test
// files, so the unit suite never depends on Lima.
package functional
