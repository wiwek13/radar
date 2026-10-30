package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Kubeconfig names the kubeconfig files sessions are drawn from. No paths
// means kubectl's own resolution: $KUBECONFIG, else ~/.kube/config.
type Kubeconfig struct {
	Paths []string
}

func (k Kubeconfig) load() (*clientcmdapi.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if len(k.Paths) > 0 {
		rules = &clientcmd.ClientConfigLoadingRules{Precedence: k.Paths}
	}
	return rules.Load()
}

// Contexts returns every context name, sorted, and the current-context.
func (k Kubeconfig) Contexts() ([]string, string, error) {
	cfg, err := k.load()
	if err != nil {
		return nil, "", err
	}
	names := make([]string, 0, len(cfg.Contexts))
	for name := range cfg.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, cfg.CurrentContext, nil
}

// WritePinned writes a kubeconfig holding only contextName, its cluster and
// its user. A session started on it has no other context to switch to, so
// nothing in its UI can point it at a different cluster.
func (k Kubeconfig) WritePinned(contextName, path string) error {
	cfg, err := k.load()
	if err != nil {
		return err
	}
	ctx, ok := cfg.Contexts[contextName]
	if !ok {
		return fmt.Errorf("context %q not found in kubeconfig", contextName)
	}
	cluster, ok := cfg.Clusters[ctx.Cluster]
	if !ok {
		return fmt.Errorf("context %q refers to missing cluster %q", contextName, ctx.Cluster)
	}
	pinned := clientcmdapi.NewConfig()
	pinned.CurrentContext = contextName
	pinned.Contexts[contextName] = ctx
	pinned.Clusters[ctx.Cluster] = cluster
	if ctx.AuthInfo != "" {
		user, ok := cfg.AuthInfos[ctx.AuthInfo]
		if !ok {
			return fmt.Errorf("context %q refers to missing user %q", contextName, ctx.AuthInfo)
		}
		pinned.AuthInfos[ctx.AuthInfo] = user
	}
	data, err := clientcmd.Write(*pinned)
	if err != nil {
		return err
	}
	return writePrivateFile(path, data)
}

// writePrivateFile writes data with owner-only permissions, replacing path
// atomically so a session never reads a half-written kubeconfig.
func writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
