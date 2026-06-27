# iceclimber — build, test, and functional-sandbox targets.
#
# Unit tests run with `make test` and never touch Lima. Functional tests
# (`make test-functional`) drive the real binary against a Lima/Alpine VM and
# are gated behind the `functional` build tag, so plain `go test ./...` skips
# them entirely. See test/README.md.

SANDBOX     := iceclimber-sandbox
SANDBOX_TPL := test/lima/sandbox.yaml
BIN         := iceclimber

.PHONY: build fmt vet test test-functional e2e sandbox-up sandbox-down sandbox-status sandbox-config sandbox-shell clean

build:
	go build -o $(BIN) .

fmt:
	gofmt -w .

vet:
	go vet ./...

# Unit suite (race detector on). No VM, no build tag.
test:
	go test -race ./...

# Functional suite: black-box probe against the Lima/Alpine VM. Requires the
# sandbox to be running (see sandbox-up); tests skip with a clear message if not.
test-functional: build
	go test -tags functional -count=1 ./test/functional/...

# One-shot: bring the sandbox up, then run the functional suite.
e2e: sandbox-up test-functional

sandbox-up:
	@limactl list --quiet 2>/dev/null | grep -qx $(SANDBOX) \
		&& echo "sandbox '$(SANDBOX)' already exists; starting if stopped" \
		&& limactl start $(SANDBOX) --tty=false \
		|| limactl start --name=$(SANDBOX) $(SANDBOX_TPL) --tty=false

sandbox-down:
	-limactl stop $(SANDBOX)
	-limactl delete $(SANDBOX)

sandbox-status:
	limactl list $(SANDBOX)

# Write an iceclimber.yaml pointing at the running sandbox (see test/PLAYGROUND.md).
sandbox-config:
	@bash test/lima/gen-config.sh $(SANDBOX) iceclimber.yaml

# Open an interactive shell inside the sandbox (Nana's view).
sandbox-shell:
	@limactl shell $(SANDBOX)

clean:
	rm -f $(BIN)
