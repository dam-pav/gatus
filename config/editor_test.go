package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const editorTestYAML = "endpoints:\n  - name: example\n    url: https://example.com\n    conditions: ['[STATUS] == 200']\n"

func TestConfigurationEditorValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, content string
		valid         bool
	}{
		{"empty fragment", "", true},
		{"comment fragment", "# configuration fragment\n", true},
		{"valid", editorTestYAML, true},
		{"unknown root", editorTestYAML + "endponts: []\n", false},
		{"unknown nested", strings.Replace(editorTestYAML, "    url:", "    typo: true\n    url:", 1), false},
		{"wrong scalar type", editorTestYAML + "metrics: 'true'\n", false},
		{"invalid duration", strings.Replace(editorTestYAML, "    url:", "    interval: tomorrow\n    url:", 1), false},
		{"duration", strings.Replace(editorTestYAML, "    url:", "    interval: 1m30s\n    url:", 1), true},
		{"null endpoint", "endpoints: [null]\n", false},
		{"multiple documents", editorTestYAML + "---\nmetrics: true\n", false},
		{"duplicate keys", editorTestYAML + "metrics: true\nmetrics: false\n", false},
		{"inline provider", editorTestYAML + "alerting:\n  slack:\n    webhook-url: https://example.com\n    overrides:\n      - group: example\n        webhook-url: https://example.org\n", true},
		{"custom bark override", editorTestYAML + "alerting:\n  bark:\n    device-key: test\n    overrides:\n      - group: example\n        device-key: other\n", true},
		{"inline tunnel map", editorTestYAML + "tunneling:\n  named:\n    type: ssh\n    host: example.com\n    username: test\n", true},
		{"dynamic maps", editorTestYAML + "suites:\n  - name: test\n    context:\n      arbitrary: {nested: [1, true]}\n", true},
		{"timestamp", editorTestYAML + "announcements:\n  - timestamp: 2026-11-14T12:00:00Z\n    message: test\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateEditorYAML([]byte(tc.content))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestConfigurationEditorSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(editorTestYAML), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfiguration(path)
	if err != nil {
		t.Fatal(err)
	}
	original, err := cfg.EditorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	invalid := strings.Replace(editorTestYAML, "https://example.com", "", 1)
	if _, err := cfg.EditConfiguration("config.yaml", invalid, original.Revision, true); err == nil {
		t.Fatal("accepted invalid endpoint")
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != editorTestYAML {
		t.Fatal("invalid save modified file")
	}
	if _, err := cfg.EditConfiguration("../other.yaml", editorTestYAML, original.Revision, true); !errors.Is(err, ErrEditorFile) {
		t.Fatalf("traversal: %v", err)
	}
	t.Setenv("GATUS_EDITOR_TEST_INTERVAL", "30s")
	updated := editorTestYAML + "# preserved comment\nconcurrency: 4\n"
	updated = strings.Replace(updated, "    url:", "    interval: ${GATUS_EDITOR_TEST_INTERVAL}\n    url:", 1)
	if _, err := cfg.EditConfiguration("config.yaml", updated, original.Revision, false); err != nil {
		t.Fatal(err)
	}
	unchanged, _ = os.ReadFile(path)
	if string(unchanged) != editorTestYAML {
		t.Fatal("validate modified file")
	}
	saved, err := cfg.EditConfiguration("config.yaml", updated, original.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != updated {
		t.Fatal("save did not preserve raw YAML")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("permissions changed")
	}
	if !cfg.HasLoadedConfigurationBeenModified() {
		t.Fatal("save was not visible to watcher")
	}
	if saved.Revision == original.Revision {
		t.Fatal("revision unchanged")
	}
	if cfg.Concurrency == 4 {
		t.Fatal("live config was mutated")
	}
	if _, err := cfg.EditConfiguration("config.yaml", editorTestYAML, original.Revision, true); !errors.Is(err, ErrEditorConflict) {
		t.Fatalf("stale save: %v", err)
	}
}

func TestConfigurationEditorDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "endpoints.yaml"), []byte(editorTestYAML), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "ui.yml"), []byte("ui:\n  title: Test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.yaml"), []byte("# empty fragment\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfiguration(dir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := cfg.EditorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Files) != 3 {
		t.Fatalf("files: %+v", snapshot.Files)
	}
	saved, err := cfg.EditConfiguration("nested/ui.yml", "ui:\n  title: Updated\n", snapshot.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfiguration(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "endpoints.yaml"), []byte(editorTestYAML+"metrics: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.EditConfiguration("nested/ui.yml", "ui: {}\n", saved.Revision, true); !errors.Is(err, ErrEditorConflict) {
		t.Fatalf("did not catch another file changing: %v", err)
	}
}
