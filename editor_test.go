package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ruca-radio/netgcfg/rax"
)

// TestEditorFlattenEditRoundTrip verifies the editor flattens the config into
// leaves, applies an edit in place, and the edit survives a JSON round-trip.
func TestEditorFlattenEditRoundTrip(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "NETGEAR_RAX29.cfg"))
	if err != nil {
		t.Skipf("no live config: %v", err)
	}
	c, err := rax.Decrypt(b)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	js, err := c.ToJSON(false)
	if err != nil {
		t.Fatalf("tojson: %v", err)
	}

	e := newRAXEditor(js, 100, 40)
	if len(e.rows) == 0 {
		t.Fatal("editor flattened zero rows")
	}

	// Edit the first leaf.
	target := e.rows[0].path
	e.selected = target
	e.editBox = "CHANGED_TEST_VALUE"
	e.applyEdit()

	out, err := e.toJSON()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	if _, err := rax.FromJSON(out); err != nil {
		t.Fatalf("re-parse edited json: %v", err)
	}

	// Re-flatten from the edited JSON and confirm the value stuck.
	e2 := newRAXEditor(out, 100, 40)
	found := false
	for _, r := range e2.rows {
		if r.path == target {
			found = true
			if r.value != "CHANGED_TEST_VALUE" {
				t.Fatalf("edit not persisted: got %q at %q", r.value, target)
			}
		}
	}
	if !found {
		t.Fatalf("target %q missing after round-trip", target)
	}
}

// TestEditorFilterMatches verifies substring filtering hits paths and values.
func TestEditorFilterMatches(t *testing.T) {
	e := &raxEditor{rows: []leaf{
		{path: "Device.WiFi.SSID[0].SSID", value: "Hotspot"},
		{path: "Device.Time.NTPServer1", value: "10.27.27.1"},
	}}
	e.filter = "ntp"
	vis := e.visible()
	if len(vis) != 1 || vis[0] != 1 {
		t.Fatalf("filter 'ntp' should match row 1 only, got %v", vis)
	}
}
