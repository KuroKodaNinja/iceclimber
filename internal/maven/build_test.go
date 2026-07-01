package maven

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

func TestParseMvnVersion(t *testing.T) {
	out := "Apache Maven 3.9.12 (848fbb4bf2d427b72bdb2471c22fced7ebd9a7a1)\n" +
		"Maven home: /nix/store/…/maven\n" +
		"Java version: 25.0.3, vendor: Azul\n"
	if got := parseMvnVersion(out); got != "3.9.12" {
		t.Errorf("parseMvnVersion = %q, want 3.9.12", got)
	}
	// No parenthetical (trailing version only).
	if got := parseMvnVersion("Apache Maven 3.9.9"); got != "3.9.9" {
		t.Errorf("parseMvnVersion(bare) = %q, want 3.9.9", got)
	}
	if got := parseMvnVersion("not maven output"); got != "" {
		t.Errorf("parseMvnVersion(garbage) = %q, want empty", got)
	}
}

func TestMavenToolURL(t *testing.T) {
	got := mavenToolURL("3.9.12")
	for _, want := range []string{"archive.apache.org", "maven-3/3.9.12/", "apache-maven-3.9.12-bin.tar.gz"} {
		if !strings.Contains(got, want) {
			t.Errorf("mavenToolURL missing %q: %s", want, got)
		}
	}
}

func TestBuildHandlerErrors(t *testing.T) {
	h := BuildHandler(BuildDeps{})
	if r := h(context.Background(), protocol.Request{ID: "1", Params: json.RawMessage("{")}); r.Error == nil || r.Error.Code != "malformed_params" {
		t.Errorf("malformed params: got %+v", r.Error)
	}
	if r := h(context.Background(), protocol.Request{ID: "2", Params: json.RawMessage(`{"java_version":"21"}`)}); r.Error == nil || r.Error.Code != "missing_project" {
		t.Errorf("missing project: got %+v", r.Error)
	}
	if r := h(context.Background(), protocol.Request{ID: "3", Params: json.RawMessage(`{"project":"/p"}`)}); r.Error == nil || r.Error.Code != "missing_java_version" {
		t.Errorf("missing java_version: got %+v", r.Error)
	}
}
