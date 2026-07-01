package npm

import (
	"reflect"
	"testing"
)

func TestManifestDeps(t *testing.T) {
	manifest := []byte(`{
		"name": "app", "version": "1.0.0",
		"dependencies": { "blessed-contrib": "4.11.0", "blessed": "0.1.81" },
		"devDependencies": { "typescript": "5.4.0" }
	}`)
	got, err := manifestDeps(manifest)
	if err != nil {
		t.Fatalf("manifestDeps: %v", err)
	}
	// dependencies + devDependencies, deduped, sorted.
	want := []string{"blessed", "blessed-contrib", "typescript"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("manifestDeps = %v, want %v", got, want)
	}
	// A manifest with no deps yields an empty (not error) list.
	if names, err := manifestDeps([]byte(`{"name":"x"}`)); err != nil || len(names) != 0 {
		t.Errorf("empty deps = %v, %v; want []", names, err)
	}
	// Malformed JSON errors.
	if _, err := manifestDeps([]byte("{")); err == nil {
		t.Error("malformed package.json should error")
	}
}
