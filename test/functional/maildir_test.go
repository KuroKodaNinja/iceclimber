//go:build functional

package functional

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestMaildirGC is the #6 regression: the maildir no longer grows without bound and
// the "awaiting collection" count is real. It proves, against a live serving Popo with
// the freshly-built popo client: (a) a bootstrap leaves inbox/new empty (the smoke pong
// is collected + pruned); (b) a ping round-trips, popo auto-collects, and GC prunes the
// pair so inbox/new and outbox/cur drain to 0; (c) the retention sweep reaps an old
// uncollected response while leaving a recent one.
func TestMaildirGC(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-gc-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)
	scheduleRootCleanup(t, root)
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	countDir := func(dir string) int {
		out := strings.TrimSpace(limaSh(t, "ls -1 "+root+"/protocol/"+dir+" 2>/dev/null | wc -l"))
		n, _ := strconv.Atoi(out)
		return n
	}
	existsNew := func(name string) bool {
		return strings.Contains(limaSh(t, "test -f "+root+"/protocol/inbox/new/"+name+".json && echo yes || echo no"), "yes")
	}

	// (a) After bootstrap the smoke-test pong was collected + GC'd.
	if n := countDir("inbox/new"); n != 0 {
		t.Errorf("after bootstrap inbox/new = %d, want 0 (smoke pong should be collected+pruned)", n)
	}

	startServe(t, cfg) // background serve under a private HOME

	// (b) A ping round-trips; popo auto-collects; GC prunes the pair within a cycle.
	if out := limaSh(t, root+"/popo ping 2>&1"); !strings.Contains(out, "bridge up") {
		t.Fatalf("popo ping = %q, want 'bridge up …'", strings.TrimSpace(out))
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if countDir("inbox/new") == 0 && countDir("outbox/cur") == 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if n := countDir("inbox/new"); n != 0 {
		t.Errorf("after a collected ping, inbox/new = %d, want 0 (the #6 regression)", n)
	}
	if n := countDir("outbox/cur"); n != 0 {
		t.Errorf("after a collected ping, outbox/cur = %d, want 0", n)
	}

	// (c) Retention (1h default): an OLD uncollected response is reaped; a RECENT one survives.
	writeSynthetic := func(id, completedAt string) {
		env := `{"schema_version":1,"id":"` + id + `","status":"ok","completed_at":"` + completedAt + `","result":{}}`
		limaSh(t, "printf '%s' "+remoteQuote(env)+" > "+root+"/protocol/inbox/new/"+id+".json")
	}
	writeSynthetic("oldsynthetic", time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339))
	writeSynthetic("recentsynthetic", time.Now().UTC().Format(time.RFC3339))

	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !existsNew("oldsynthetic") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if existsNew("oldsynthetic") {
		t.Error("an old uncollected response (>1h) was not reaped by the retention sweep")
	}
	if !existsNew("recentsynthetic") {
		t.Error("a recently-delivered uncollected response was wrongly reaped by the retention sweep")
	}
}
