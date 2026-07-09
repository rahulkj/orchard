---
name: orchard
description: Build, run, and extend orchard — the Go CLI in this repo that creates/deletes/scales Kubernetes clusters where every node is an apple/container VM (kubeadm-driven, no minikube/kind/kiac dependency). Use whenever the user wants a local Kubernetes cluster on macOS via Apple's container runtime, wants to add a feature to orchard itself, or hits a networking/CNI/addon-install problem inside a node VM. Covers the CLI's commands, verified working configuration, confirmed-broken configuration, and the guest-has-no-internet-egress workaround pattern that every network-dependent addon in this codebase uses.
user-invocable: true
---

# orchard — Kubernetes on apple/container

This repo is a standalone Go CLI (module `github.com/rahulkj/orchard`) that drives `kubeadm`
against node VMs booted with Apple's `container` CLI. It replaces third-party/unofficial
alternatives (kiac, minikube — neither has real apple/container support) with something owned
and understood end-to-end.

## Building and prerequisites

```bash
container system start                       # apple/container first-time init
container system kernel set --recommended    # if prompted
cd <this repo> && go build -o orchard .
./orchard doctor                              # checks apple/container, system service, kubectl
```

## Commands

- `orchard create --name dev --workers 2 [--cni kindnet|flannel|calico] [--headlamp] [--proxy-forward] [--image ...] [--cp-cpus] [--cp-memory] [--worker-cpus] [--worker-memory] [--no-metrics] [--no-storage]`
- `orchard scale --name dev --workers N` — up: boots+joins new workers reusing lowest free indices; down: drains + `kubectl delete node` + VM removal on highest-indexed workers first
- `orchard delete --name dev` — removes node VMs, kubeconfig entries, and `~/.orchard/clusters/<name>.json` state
- `orchard list` / `orchard doctor`
- `orchard check-updates` / `orchard upgrade --name dev [--image] [--yes]` — upgrade is a **destroy-and-recreate**, not in-place (see below)
- `orchard headlamp install --name dev` / `orchard headlamp token --name dev`
- `orchard self-update [--repo owner/name]`

Full flag table and design rationale: `README.md` in this repo.

## Verified facts — trust these over re-deriving them

- **kindnet is the only CNI confirmed working**, including cross-node pod connectivity
  (tested live with a 2-node cluster + pods pinned to each node via `nodeName`).
- **flannel is confirmed broken**, empirically: `kubectl describe pod` shows
  `failed to find plugin "bridge" in path [/opt/cni/bin]`. Flannel's CNI config delegates
  pod interface wiring to the standard CNI `bridge` plugin, which the `kindest/node` image
  doesn't ship (only kindnet's plugin binaries are present). This is a missing-binary problem,
  not a kernel-module problem — don't reintroduce the "needs VXLAN/br_netfilter" theory without
  re-testing; that was disproven.
- **calico is untested**, not confirmed either way. It wires interfaces itself (no delegation
  to `bridge`), so it might avoid flannel's exact failure — don't assume it works either.
- **Headlamp's official manifest does not create the ServiceAccount its bundled Secret
  references** — `internal/k8s/headlamp.go` creates `headlamp-admin` + a `ClusterRoleBinding`
  to `cluster-admin` separately. Access is verified end-to-end: install → `kubectl create
  token` → `kubectl port-forward -n kube-system service/headlamp 8080:80` → HTTP 200.
- **Docker Hub's tags API ignores `ordering=` and returns empty `last_updated` for this
  (org-owned) repo.** `internal/nodeimage/discover.go` pages through the full tag list and
  computes the max by parsing semver client-side — don't reintroduce API-side ordering.
- **`kubeadm init` is pinned to `--kubernetes-version` read from the node image's own
  `/kind/version` file.** Without this, kubeadm's "resolve latest stable from the internet"
  check is flaky in this network environment and can return a version newer than what's
  baked into the image, forcing a network pull that fails on an untrusted certificate inside
  the guest. If you add another kubeadm-driven code path, pin the version the same way.

## The guest-has-no-internet-egress pattern

Node VMs cannot reach arbitrary internet hosts (`apple/container` pulls images host-side, so
the guest never needs to). Anything that would otherwise do `kubectl apply -f <url>` *inside*
the guest — metrics-server, flannel/calico, Headlamp — instead fetches the manifest **on the
host** (`net/http.Get`) and pipes it in:

```go
resp, _ := http.Get(manifestURL)
m.rt.ExecStdin(cp, resp.Body, "kubectl", "--kubeconfig", adminConf, "apply", "-f", "-")
```

Follow this exact pattern for any new addon that needs a manifest from a URL. See
`internal/k8s/cni.go`'s `applyManifestFromHost` and `internal/k8s/headlamp.go`.

## proxy-forward specifics

`--proxy-forward` (`internal/k8s/proxy.go`, `internal/hostproxy/`) does two independent things
per node, because a transparent TLS-intercepting corporate security agent and an explicit
`HTTP_PROXY` are different problems:

1. Always: exports certs from macOS `System.keychain` (not `SystemRootCertificates.keychain` —
   the guest already trusts Apple's ~150 default roots; `System.keychain` is where MDM/security
   agents install their own root) and runs `update-ca-certificates` in the guest.
2. If `HTTP_PROXY`/`HTTPS_PROXY` env vars are set on the host: forwards them into
   `/etc/environment` and a systemd drop-in for `containerd`, rewriting `127.0.0.1`/`localhost`
   to the guest's default gateway.

`scutil --proxy`/PAC auto-config is deliberately not read — on managed Macs it typically points
at a loopback port a guest VM can't reach anyway, and evaluating a PAC script needs a JS engine.

## When extending orchard

- Cluster settings are persisted at `~/.orchard/clusters/<name>.json` (`internal/state`) so
  `scale`/`upgrade` can recreate a cluster's original configuration. Add new `CreateConfig`
  fields to `state.Cluster` and `CreateConfig.toState()` together, or `upgrade` will silently
  drop the new setting on recreate.
- Container naming: `orchard-<cluster>-control-plane`, `orchard-<cluster>-worker-<i>` — see
  `internal/k8s/naming.go`. Kubeconfig context/cluster/user entries are named `orchard-<cluster>`.
- `internal/containerrt` is the only package that shells out to the `container` CLI — route
  any new apple/container interaction through it rather than calling `exec.Command("container",
  ...)` elsewhere.
