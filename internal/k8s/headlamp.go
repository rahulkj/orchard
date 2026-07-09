package k8s

import "strings"

// headlampManifestURL is kubernetes-sigs/headlamp's official in-cluster
// manifest (the project was donated to Kubernetes SIGs; headlamp-k8s/headlamp
// now redirects here). It creates a Deployment + Service named "headlamp"
// in kube-system, but -- as of this writing -- does not create the
// ServiceAccount its bundled Secret references, so headlampRBAC creates it
// separately.
const headlampManifestURL = "https://raw.githubusercontent.com/kubernetes-sigs/headlamp/main/kubernetes-headlamp.yaml"

const headlampServiceAccount = "headlamp-admin"

const headlampRBAC = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: headlamp-admin
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: headlamp-admin
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
  - kind: ServiceAccount
    name: headlamp-admin
    namespace: kube-system
`

// installHeadlamp applies the upstream manifest (fetched host-side, same
// workaround as installMetricsServer) plus a headlamp-admin ServiceAccount
// bound to cluster-admin -- appropriate for a local dev cluster, not a
// shared one -- so HeadlampToken has something to mint tokens for.
func (m *Manager) installHeadlamp(cp string) error {
	if err := m.applyManifestFromHost(cp, headlampManifestURL); err != nil {
		return err
	}
	return m.rt.ExecStdin(cp, strings.NewReader(headlampRBAC),
		"kubectl", "--kubeconfig", adminConf, "apply", "-f", "-")
}

// HeadlampToken mints a bearer token for the headlamp-admin service account,
// installing Headlamp first if it isn't already present.
func (m *Manager) HeadlampToken(cluster string) (string, error) {
	cp := ControlPlane(cluster)
	if err := m.installHeadlamp(cp); err != nil {
		return "", err
	}
	out, err := m.rt.Exec(cp, "kubectl", "--kubeconfig", adminConf,
		"create", "token", headlampServiceAccount, "-n", "kube-system", "--duration=8760h")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
