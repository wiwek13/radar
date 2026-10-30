package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

const twoClusterKubeconfig = `apiVersion: v1
kind: Config
current-context: dev
clusters:
- name: dev-cluster
  cluster:
    server: https://dev.example.invalid
    certificate-authority: certs/dev-ca.crt
- name: prod-cluster
  cluster:
    server: https://prod.example.invalid
    certificate-authority-data: UFJPRA==
contexts:
- name: dev
  context:
    cluster: dev-cluster
    user: dev-user
    namespace: team-a
- name: prod
  context:
    cluster: prod-cluster
    user: prod-user
users:
- name: dev-user
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: aws
      args: [eks, get-token, --cluster-name, dev]
      env:
      - name: AWS_PROFILE
        value: dev-profile
- name: prod-user
  user:
    token: prod-secret-token
`

func writeKubeconfig(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(twoClusterKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func TestContextsListsNamesAndCurrent(t *testing.T) {
	_, path := writeKubeconfig(t)
	names, current, err := Kubeconfig{Paths: []string{path}}.Contexts()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "dev,prod" || current != "dev" {
		t.Fatalf("Contexts() = %v, %q", names, current)
	}
}

func TestWritePinnedHoldsOnlyTheOneContext(t *testing.T) {
	dir, path := writeKubeconfig(t)
	out := filepath.Join(t.TempDir(), "session", "kubeconfig")

	if err := (Kubeconfig{Paths: []string{path}}).WritePinned("dev", out); err != nil {
		t.Fatalf("WritePinned: %v", err)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"prod", "prod-secret-token", "UFJPRA=="} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("pinned kubeconfig contains %q from another context:\n%s", leaked, raw)
		}
	}

	cfg, err := clientcmd.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Contexts) != 1 || len(cfg.Clusters) != 1 || len(cfg.AuthInfos) != 1 {
		t.Fatalf("want exactly one context/cluster/user, got %d/%d/%d", len(cfg.Contexts), len(cfg.Clusters), len(cfg.AuthInfos))
	}
	if cfg.CurrentContext != "dev" || cfg.Contexts["dev"].Namespace != "team-a" {
		t.Fatalf("context not preserved: current=%q ctx=%+v", cfg.CurrentContext, cfg.Contexts["dev"])
	}
	if got, want := cfg.Clusters["dev-cluster"].CertificateAuthority, filepath.Join(dir, "certs", "dev-ca.crt"); got != want {
		t.Fatalf("relative CA path = %q, want it resolved to %q", got, want)
	}
	exec := cfg.AuthInfos["dev-user"].Exec
	if exec == nil || exec.Command != "aws" || len(exec.Env) != 1 || exec.Env[0].Value != "dev-profile" {
		t.Fatalf("exec credential plugin not preserved: %+v", exec)
	}

	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("pinned kubeconfig mode = %v, want 0600", info.Mode().Perm())
	}
	dirInfo, _ := os.Stat(filepath.Dir(out))
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("session dir mode = %v, want 0700", dirInfo.Mode().Perm())
	}
}

func TestWritePinnedRejectsUnknownContext(t *testing.T) {
	_, path := writeKubeconfig(t)
	out := filepath.Join(t.TempDir(), "kubeconfig")
	if err := (Kubeconfig{Paths: []string{path}}).WritePinned("staging", out); err == nil {
		t.Fatal("WritePinned accepted a context that does not exist")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("WritePinned left a file behind for an unknown context: %v", err)
	}
}
