package k8s

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func kubeconfigPath() (string, error) {
	if env := os.Getenv("KUBECONFIG"); env != "" && !strings.Contains(env, string(os.PathListSeparator)) {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".kube", "config"), nil
}

// rewriteAdminConf renames admin.conf's cluster/user/context to
// orchard-<cluster> and points the server at the control-plane's routable IP.
func rewriteAdminConf(cluster, adminConf, serverIP string) (clusterEntry, userEntry, contextEntry map[string]any, err error) {
	entry := "orchard-" + cluster

	var admin map[string]any
	if err := yaml.Unmarshal([]byte(adminConf), &admin); err != nil {
		return nil, nil, nil, fmt.Errorf("parsing admin.conf: %w", err)
	}

	clusters, _ := admin["clusters"].([]any)
	users, _ := admin["users"].([]any)
	if len(clusters) == 0 || len(users) == 0 {
		return nil, nil, nil, fmt.Errorf("admin.conf missing clusters or users")
	}

	var ok bool
	clusterEntry, ok = clusters[0].(map[string]any)
	if !ok {
		return nil, nil, nil, fmt.Errorf("admin.conf cluster entry has unexpected shape")
	}
	if body, ok := clusterEntry["cluster"].(map[string]any); ok {
		body["server"] = fmt.Sprintf("https://%s:6443", serverIP)
	}
	clusterEntry["name"] = entry

	userEntry, ok = users[0].(map[string]any)
	if !ok {
		return nil, nil, nil, fmt.Errorf("admin.conf user entry has unexpected shape")
	}
	userEntry["name"] = entry

	contextEntry = map[string]any{
		"name": entry,
		"context": map[string]any{
			"cluster": entry,
			"user":    entry,
		},
	}
	return clusterEntry, userEntry, contextEntry, nil
}

// MergeKubeconfig rewrites the cluster's admin.conf entries to orchard-<name>
// and merges them into the user's kubeconfig (creating it if absent),
// switching current-context to the new cluster. Returns the file path.
func MergeKubeconfig(cluster, adminConf, serverIP string) (string, error) {
	entry := "orchard-" + cluster
	clusterEntry, userEntry, contextEntry, err := rewriteAdminConf(cluster, adminConf, serverIP)
	if err != nil {
		return "", err
	}

	path, err := kubeconfigPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}

	dest := map[string]any{"apiVersion": "v1", "kind": "Config"}
	if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &dest); err != nil {
			return "", fmt.Errorf("parsing existing kubeconfig %s: %w", path, err)
		}
	}

	dest["clusters"] = upsert(listOf(dest["clusters"]), entry, clusterEntry)
	dest["users"] = upsert(listOf(dest["users"]), entry, userEntry)
	dest["contexts"] = upsert(listOf(dest["contexts"]), entry, contextEntry)
	dest["current-context"] = entry

	out, err := yaml.Marshal(dest)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// RemoveKubeconfig strips a cluster's entries from the user's kubeconfig and
// clears current-context if it pointed there.
func RemoveKubeconfig(cluster string) error {
	entry := "orchard-" + cluster
	path, err := kubeconfigPath()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var dest map[string]any
	if err := yaml.Unmarshal(raw, &dest); err != nil {
		return fmt.Errorf("parsing kubeconfig %s: %w", path, err)
	}
	dest["clusters"] = drop(listOf(dest["clusters"]), entry)
	dest["users"] = drop(listOf(dest["users"]), entry)
	dest["contexts"] = drop(listOf(dest["contexts"]), entry)
	if cur, _ := dest["current-context"].(string); cur == entry {
		dest["current-context"] = ""
	}
	out, err := yaml.Marshal(dest)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

func listOf(v any) []any {
	l, _ := v.([]any)
	return l
}

func nameOf(item any) string {
	m, ok := item.(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m["name"].(string)
	return s
}

func upsert(list []any, name string, item any) []any {
	for i, existing := range list {
		if nameOf(existing) == name {
			list[i] = item
			return list
		}
	}
	return append(list, item)
}

func drop(list []any, name string) []any {
	out := make([]any, 0, len(list))
	for _, existing := range list {
		if nameOf(existing) != name {
			out = append(out, existing)
		}
	}
	return out
}
