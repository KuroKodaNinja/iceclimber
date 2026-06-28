# iceclimber — build, test, and functional-sandbox targets.
#
# Unit tests run with `make test` and never touch Lima. Functional tests
# (`make test-functional`) drive the real binary against a Lima/Alpine VM and
# are gated behind the `functional` build tag, so plain `go test ./...` skips
# them entirely. See test/README.md.

SANDBOX     := iceclimber-sandbox
SANDBOX_TPL := test/lima/sandbox.yaml
DEMO        := iceclimber-demo
DEMO_TPL    := test/lima/demo.yaml
DEMO_CFG    := .demo/config.yaml
BIN         := iceclimber

.PHONY: build fmt vet test test-functional scenario e2e sandbox-up sandbox-down sandbox-status sandbox-config sandbox-shell \
	demo-up demo-down demo-status demo-firewall demo-firewall-down demo-shell \
	demo demo-live demo-config demo-bootstrap demo-agent demo-verify demo-reset demo-logs clean

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

# Self-contained, full-stack "build a real application" scenarios (one per
# language) under test/scenarios/. Gated by the `scenario` build tag; needs the
# sandbox up (and, for relay-based scenarios, npm on this host). See
# test/scenarios/README.md.
scenario: build sandbox-up
	go test -tags scenario -count=1 -timeout 20m ./test/scenarios/...

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

# --- Acceptance demo: a real Claude agent in an air-gapped sandbox (see DEMO.md) ---

# Fully-automated acceptance demo (the CI gate): boots the VM and runs the
# //go:build demo test, which drives a real Claude agent through the whole flow
# and asserts the result. Needs CLAUDE_CODE_OAUTH_TOKEN (subscription, not API);
# skips cleanly without it. Opt-in via the `demo` tag — never part of `make test`.
demo: build demo-up
	go test -tags demo -count=1 -timeout 30m ./test/demo/...

# Operator-driven demo: watch the agent work and approve its egress live. Sets up
# + air-gaps the VM, then runs a guided two-pass flow that pauses for you to
# approve the held fetch. Needs CLAUDE_CODE_OAUTH_TOKEN. See DEMO.md.
demo-live: build demo-up
	@bash test/lima/demo-live.sh $(DEMO)

# Boot + provision the demo VM (Alpine + Claude Code). First boot installs node,
# the agent, and its musl deps while the network is still open.
demo-up:
	@limactl list --quiet 2>/dev/null | grep -qx $(DEMO) \
		&& echo "demo '$(DEMO)' already exists; starting if stopped" \
		&& limactl start $(DEMO) --tty=false \
		|| limactl start --name=$(DEMO) $(DEMO_TPL) --tty=false

demo-down:
	-limactl stop $(DEMO)
	-limactl delete $(DEMO)

demo-status:
	limactl list $(DEMO)

# Air-gap the demo VM down to only the Claude API (DNS + 443 to Anthropic).
# After this, the agent inside can reach nothing but its own API — it MUST
# bridge through Popo for Python, packages, and web data.
demo-firewall:
	@limactl shell $(DEMO) -- sudo sh -s up < test/lima/demo-firewall.sh

# Restore open egress (reset between runs).
demo-firewall-down:
	@limactl shell $(DEMO) -- sudo sh -s down < test/lima/demo-firewall.sh

# Open an interactive shell inside the demo VM (the agent's view).
demo-shell:
	@limactl shell $(DEMO)

# Write the demo config (.demo/config.yaml) pointing at the demo VM, with
# remote_root pinned to a predictable tree ($HOME/iceclimber-demo) so the agent
# brief can name paths.
demo-config:
	@root=$$(limactl shell $(DEMO) -- sh -lc 'echo $$HOME/iceclimber-demo'); \
	 bash test/lima/gen-config.sh $(DEMO) $(DEMO_CFG) $$root

# Create the protocol tree + drop NANA.md in the demo VM.
demo-bootstrap: build demo-config
	./$(BIN) bootstrap --config $(DEMO_CFG)

# Launch the agent in the (air-gapped) demo VM. Needs CLAUDE_CODE_OAUTH_TOKEN
# (subscription) and Popo serving in another terminal. See DEMO.md.
demo-agent:
	@bash test/lima/demo-agent.sh $(DEMO)

# Check the agent's program renders the data it fetched through Popo.
demo-verify:
	@bash test/lima/demo-verify.sh $(DEMO)

# Tail the merged host (Popo) + sandbox (agent) activity during a demo run.
# Run in a separate terminal alongside `make demo-live`.
demo-logs: build
	@./$(BIN) logs -f --config $(DEMO_CFG) --agent-log $$HOME/.iceclimber/$(DEMO)/agent.log

# Clear the protocol maildir + work dir for a fresh agent pass. Keeps the
# installed runtimes (so re-runs are fast) and any egress approvals. Use this
# between a headless run that was held at the gate and the approved re-run —
# the maildir dedup won't re-service an id that already has a response.
demo-reset:
	@root=$$(limactl shell $(DEMO) -- sh -lc 'echo $$HOME/iceclimber-demo'); \
	 limactl shell $(DEMO) -- sh -c "rm -f \
	   $$root/protocol/outbox/new/* $$root/protocol/outbox/cur/* $$root/protocol/outbox/tmp/* \
	   $$root/protocol/inbox/new/*  $$root/protocol/inbox/cur/*  $$root/protocol/inbox/tmp/* \
	   $$root/work/*"; \
	 echo "demo: maildir + work cleared (runtimes + approvals kept)"

clean:
	rm -f $(BIN)
