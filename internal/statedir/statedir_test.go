package statedir

import (
	"path/filepath"
	"testing"
)

func TestDirDefaultsToDotRadarUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(EnvVar, "")

	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir() error: %v", err)
	}
	if want := filepath.Join(home, ".radar"); got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestDirHonorsAbsoluteOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "session-a")
	t.Setenv(EnvVar, override+string(filepath.Separator))

	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir() error: %v", err)
	}
	if got != override {
		t.Fatalf("Dir() = %q, want %q", got, override)
	}
}

// A relative override would resolve against whatever directory Radar was
// started from, so two launches could silently share or split state.
func TestDirRejectsRelativeOverride(t *testing.T) {
	t.Setenv(EnvVar, "relative/state")

	if got, err := Dir(); err == nil {
		t.Fatalf("Dir() = %q, want an error for a relative %s", got, EnvVar)
	}
}

func TestPathJoinsUnderDir(t *testing.T) {
	override := filepath.Join(t.TempDir(), "s")
	t.Setenv(EnvVar, override)

	got, err := Path("settings.json")
	if err != nil {
		t.Fatalf("Path() error: %v", err)
	}
	if want := filepath.Join(override, "settings.json"); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}
