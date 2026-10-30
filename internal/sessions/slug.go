// Package sessions runs one Radar process per kubeconfig context behind a
// single loopback address, so each browser tab can stay on its own cluster.
//
// Every session is a separate, unmodified Radar: its own process, its own
// one-context kubeconfig, its own state directory, served under /s/<slug>/.
// The controller only starts, stops and forwards to them.
package sessions

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

const slugNameMax = 24

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Slug maps a context name to a stable URL path segment. The hash suffix keeps
// two names that sanitize alike (prod_eu and prod.eu) on different sessions.
func Slug(contextName string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(contextName) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	name := strings.TrimRight(b.String(), "-")
	if len(name) > slugNameMax {
		name = strings.TrimRight(name[:slugNameMax], "-")
	}
	if name == "" {
		name = "cluster"
	}
	sum := sha256.Sum256([]byte(contextName))
	return name + "-" + hex.EncodeToString(sum[:4])
}

// ValidSlug reports whether s has the shape Slug produces. It is a syntax
// check only; whether a context owns the slug is the caller's lookup.
func ValidSlug(s string) bool {
	return len(s) <= slugNameMax+9 && slugPattern.MatchString(s)
}
