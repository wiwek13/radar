package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareStateDirSeedsPreferencesOnce(t *testing.T) {
	seed := t.TempDir()
	for name, body := range map[string]string{
		"config.json":   `{"ai":{}}`,
		"settings.json": `{"theme":"dark"}`,
		"clusters.json": `{"version":1}`,
		"ai-runs.db":    "runs from another process",
		"mcp-port":      "9280\n",
		"timeline.db":   "events",
	} {
		if err := os.WriteFile(filepath.Join(seed, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "state")

	if err := prepareStateDir(dir, seed); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", "settings.json", "clusters.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s not seeded: %v", name, err)
		}
	}
	for _, name := range []string{"ai-runs.db", "mcp-port", "timeline.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s must never be shared into a session (stat err: %v)", name, err)
		}
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode = %v, want 0700", info.Mode().Perm())
	}

	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"theme":"light"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareStateDir(dir, seed); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if string(got) != `{"theme":"light"}` {
		t.Fatalf("an existing session's settings were overwritten by the seed: %s", got)
	}
}

func TestPrepareStateDirWithoutSeed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := prepareStateDir(dir, filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
}
