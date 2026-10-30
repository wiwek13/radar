package sessions

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// seedFiles are the user's preferences and integration settings. A new
// session starts from a copy of them; it never sees the files that hold one
// process's data (AI history, timeline, MCP port, pending usage report).
var seedFiles = []string{
	"config.json",
	"settings.json",
	"clusters.json",
	"install-id",
	"whats-new.json",
	"star.json",
}

// prepareStateDir creates a session's private state directory, seeding it
// from seedFrom (normally ~/.radar) only when the directory is new so a
// session keeps its own changes across restarts.
func prepareStateDir(dir, seedFrom string) error {
	_, err := os.Stat(dir)
	fresh := errors.Is(err, fs.ErrNotExist)
	if err != nil && !fresh {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if !fresh {
		return nil
	}
	for _, name := range seedFiles {
		data, err := os.ReadFile(filepath.Join(seedFrom, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := writePrivateFile(filepath.Join(dir, name), data); err != nil {
			return err
		}
	}
	return nil
}
