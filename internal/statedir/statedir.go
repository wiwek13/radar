// Package statedir locates the directory that holds Radar's local state:
// config, settings, AI history, the MCP port file and the rest of ~/.radar.
package statedir

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnvVar moves the state directory away from ~/.radar. A supervisor running
// several Radar processes side by side gives each one its own directory, so
// one process can never rewrite another's AI history, settings or MCP port
// file.
const EnvVar = "RADAR_STATE_DIR"

// Dir returns the state directory: $RADAR_STATE_DIR when set, else ~/.radar.
func Dir() (string, error) {
	if dir := os.Getenv(EnvVar); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("%s must be an absolute path, got %q", EnvVar, dir)
		}
		return filepath.Clean(dir), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".radar"), nil
}

// Path returns name inside the state directory.
func Path(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}
