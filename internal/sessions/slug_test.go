package sessions

import (
	"strings"
	"testing"
)

func TestSlugIsStableAndValid(t *testing.T) {
	names := []string{
		"dev-pxinfra-development-server-shared-cluster",
		"arn:aws:eks:us-east-2:123456789012:cluster/prod",
		"gke_my-project_europe-west1_prod",
		"Minikube",
		"___",
		"",
		"ünïcødé",
	}
	for _, name := range names {
		got := Slug(name)
		if got != Slug(name) {
			t.Fatalf("Slug(%q) not stable", name)
		}
		if !ValidSlug(got) {
			t.Fatalf("Slug(%q) = %q, not a valid slug", name, got)
		}
	}
}

func TestSlugSeparatesNamesThatSanitizeAlike(t *testing.T) {
	pairs := [][2]string{
		{"prod_eu", "prod.eu"},
		{"prod-eu", "PROD-EU"},
		{strings.Repeat("a", 40) + "1", strings.Repeat("a", 40) + "2"},
	}
	for _, p := range pairs {
		if Slug(p[0]) == Slug(p[1]) {
			t.Fatalf("Slug(%q) == Slug(%q) = %q", p[0], p[1], Slug(p[0]))
		}
	}
}

func TestValidSlugRejectsTraversalAndOddShapes(t *testing.T) {
	for _, s := range []string{"", "..", "a/b", "a..b", "-a", "a-", "A-1", "a b", "a%2fb", strings.Repeat("a", 40)} {
		if ValidSlug(s) {
			t.Fatalf("ValidSlug(%q) = true", s)
		}
	}
}
