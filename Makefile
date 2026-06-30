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

# Release versioning: the nearest git tag (e.g. v0.2.0), else a short commit, with a
# -dirty suffix if the tree has uncommitted changes. Override with `make release VERSION=…`.
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PKG      := github.com/KuroKodaNinja/iceclimber
LDFLAGS  := -s -w -X $(PKG)/internal/cli.version=$(VERSION)
# Controller (iceclimber) targets we ship. popo is sandbox-side only, so it stays
# linux/{amd64,arm64} (built host-independently by popo-bins) — see the `release` recipe.
RELEASE_PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64

.PHONY: build popo-bins fmt vet test test-functional tui-functional scenario e2e sandbox-up sandbox-down sandbox-status sandbox-config sandbox-shell \
	demo-up demo-down demo-status demo-firewall demo-firewall-down demo-shell \
	demo demo-live demo-console demo-config demo-bootstrap demo-agent demo-verify demo-reset demo-logs demo-tui release gh-release clean

build: popo-bins
	go build -o $(BIN) .

# Cross-compile the in-sandbox `popo` client for the platforms we relay into
# sandboxes. CGO_ENABLED=0 → a static binary with no libc linkage, so one build per
# GOARCH runs on musl (Alpine) and glibc alike. iceclimber embeds these (internal/
# popobin) and bootstrap relays the matching one in.
popo-bins:
	@mkdir -p internal/popobin/bin
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o internal/popobin/bin/popo-linux-arm64 ./cmd/popo
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o internal/popobin/bin/popo-linux-amd64 ./cmd/popo

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

# Functional validation of the console's operator-action executor (consoleOps:
# RunInstall/RunBootstrap) against the sandbox — the TUI analogue of
# test-functional. Drives the same path the console's forms feed and asserts the
# sandbox-side echo. Writes a config first; skips cleanly if the VM is unreachable.
tui-functional: build sandbox-up sandbox-config
	go test -tags functional -count=1 -timeout 20m -run TestConsole ./internal/cli/...

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

# Boot + provision the demo VM (Alpine). First boot installs only the agent's musl
# prereqs (ripgrep/bash/libstdc++/…); the Claude Code binary itself is relayed in by
# `iceclimber agent install claude`.
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

# Install the Claude agent + subscription auth into the demo VM. The controller
# relays the agent binary in (no sandbox network needed — works air-gapped). Needs
# CLAUDE_CODE_OAUTH_TOKEN set (subscription; sourced from .demo/token.env if present).
# demo-live/demo do it inline; this target is for the manual step-by-step flow.
demo-agent-install: build
	@[ -n "$$CLAUDE_CODE_OAUTH_TOKEN" ] || { [ -f .demo/token.env ] && . .demo/token.env; }; \
	 CLAUDE_CODE_OAUTH_TOKEN="$$CLAUDE_CODE_OAUTH_TOKEN" ./$(BIN) agent install claude --config $(DEMO_CFG)

# Launch the agent in the (air-gapped) demo VM. The agent + its subscription auth
# were installed by demo-agent-install; this sources that env file. See DEMO.md.
demo-agent:
	@bash test/lima/demo-agent.sh $(DEMO)

# Check the agent's program renders the data it fetched through Popo.
demo-verify:
	@bash test/lima/demo-verify.sh $(DEMO)

# Tail the merged host (Popo) + sandbox (agent) activity during a demo run.
# Run in a separate terminal alongside `make demo-live`. No --agent-log needed: the
# serving Popo bridges the sandbox agent stream into the default agent.log it tails.
demo-logs: build
	@./$(BIN) logs -f --config $(DEMO_CFG)

# The graphical version of demo-logs: a live [POPO]/[NANA] dashboard.
demo-tui: build
	@./$(BIN) tui --config $(DEMO_CFG)

# The full operator console for the demo VM: bare iceclimber serves the sandbox and
# handles approvals inline. Run `make demo-agent` in another terminal and approve
# the modals here. (Needs the gate air-gapped/cleared like make demo-live.)
demo-console: build
	@./$(BIN) --config $(DEMO_CFG)

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

# --- Release: cross-built binaries for distribution (see README) ---

# Cross-build iceclimber for every controller platform + ship the sandbox-side popo
# clients, packaged with checksums into dist/. Depends on popo-bins so every
# iceclimber embeds BOTH linux popo clients (the relayed one is chosen by the
# sandbox's fingerprint at bootstrap, not the host's arch — so a mac build bootstraps
# a linux/amd64 or /arm64 VM all the same). CGO_ENABLED=0 → static, reliably
# cross-compiled binaries. The version is stamped into `iceclimber version`.
release: popo-bins
	@command -v shasum >/dev/null || command -v sha256sum >/dev/null || { echo "need shasum or sha256sum" >&2; exit 1; }
	@rm -rf dist && mkdir -p dist
	@echo "iceclimber $(VERSION)"
	@set -e; for p in $(RELEASE_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "  build iceclimber $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BIN) . ; \
		tar -C dist -czf dist/iceclimber_$(VERSION)_$${os}_$${arch}.tar.gz $(BIN); \
		rm -f dist/$(BIN); \
	done
	@set -e; for arch in arm64 amd64; do \
		echo "  package popo linux/$$arch"; \
		cp internal/popobin/bin/popo-linux-$$arch dist/popo; \
		tar -C dist -czf dist/popo_$(VERSION)_linux_$${arch}.tar.gz popo; \
		rm -f dist/popo; \
	done
	@cd dist && { command -v sha256sum >/dev/null && sha256sum *.tar.gz || shasum -a 256 *.tar.gz; } > SHA256SUMS
	@echo "release artifacts ($(VERSION)):"; ls -1 dist/

# Publish the dist/ artifacts to a GitHub release for $(VERSION). Run from a tagged
# commit (`git tag v0.2.0 && git push --tags`), having built `make release` first.
# `gh release create` creates the GitHub release (and the tag, if missing) and
# uploads the tarballs + checksums. Publishing is outward-facing — intentionally a
# separate, explicit step, never part of `release`.
gh-release: release
	@command -v gh >/dev/null || { echo "gh CLI not found — install it or upload dist/* manually" >&2; exit 1; }
	gh release create $(VERSION) dist/*.tar.gz dist/SHA256SUMS --title $(VERSION) --generate-notes

clean:
	rm -f $(BIN)
	rm -rf dist
