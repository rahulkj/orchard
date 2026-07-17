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
- `orchard stop --name dev` / `orchard start --name dev` — stop/start every node VM in place (no delete); `start` also repairs the IP-change fallout described below
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
- **`orchard stop`/`orchard start` (`internal/k8s/cluster.go`'s `Manager.Stop`/`Manager.Start`)
  work around a real `kindest/node` bug, verified live by repeatedly stop/starting a running
  2-worker cluster.** Node VMs get a fresh DHCP lease every boot. The image's own entrypoint
  tries to handle that by regenerating the API server's serving cert via `kubeadm init phase
  certs apiserver --config /kind/kubeadm.conf` — a file only `kind`'s own cluster tooling
  writes; orchard's `kubeadm init` uses flags, not `--config`, so that file never exists here.
  That command's failure, under the entrypoint's `set -o errexit`, kills the *entire* boot (the
  VM lands back in `stopped`, not just delayed) — confirmed via `container logs` showing the
  entrypoint dying right at `kubeadm init phase certs apiserver`. `startNode` retries the VM
  boot itself (not just a readiness poll) a few times to ride this out; a bare `container start`
  retry reliably recovers. Separately, that same entrypoint fixup only rewrites a node's
  references to its *own* address (never a worker's reference to the control plane's address,
  and never `admin.conf`/`super-admin.conf` at all) — confirmed by finding a worker's
  `kubelet.conf` still pointing at the cluster's IP from creation time, hours and several
  restarts later, while control-plane-local files were current. `repairAPIServerCert` and
  `repointWorkerKubelet` fix these: regenerate the apiserver cert for the current IP, repoint
  `admin.conf`/`super-admin.conf` and every worker's `kubelet.conf` at it, restart each worker's
  kubelet. Don't remove these steps to "simplify" `Start` — without them the control plane
  crash-loops or workers sit `NotReady` forever after any restart where the IP changed, which on
  this host is effectively every restart.
- **A fourth, distinct instance of the same bug class lives in the cluster-wide `kube-proxy`
  ConfigMap, not on any node's disk** — confirmed live: after the three per-node fixes above,
  nodes went `Ready` but CoreDNS sat forever logging `"Plugins not ready: kubernetes"`. Root
  cause: `kube-proxy`'s ConfigMap (`kubeconfig.conf` key) bakes in the control plane's IP *at
  kubeadm-init time* and nothing ever rewrites it on restart, since it's a Kubernetes API
  object, not a file kind's boot-time sed script could touch. Stale kube-proxy can't reach the
  API server at all, so it never programs a single Service iptables rule — confirmed via
  `iptables -t nat -L` showing zero rules for `10.96.0.1` — which breaks every ClusterIP in the
  cluster, not just `kubernetes`. That's the actual reason CoreDNS never becomes `Ready`: its
  `kubernetes` plugin talks to the API through the ClusterIP. `repairKubeProxyConfig` fixes the
  ConfigMap (piping `kubectl get cm -o yaml` through `sed` and back through `kubectl replace -f
  -`, since that sidesteps JSON-escaping the multi-line value a strategic/JSON patch would need)
  and bounces kube-proxy; `restartCoreDNS` separately force-deletes CoreDNS's pods because its
  in-process client latches onto its first (broken) connection attempt and never recovers on
  its own even once Service routing starts working again — confirmed live: fixing kube-proxy
  alone left CoreDNS logging the same "not ready" message forever until its pods were bounced.
  **Ordering matters**: both repairs run in `Start` only *after* every node VM is up. kube-proxy
  is a DaemonSet spread across every node; deleting a pod whose node is still stopped wedges it
  in `Terminating` forever waiting for a kubelet that isn't there to confirm it — this actually
  happened once during development and hung the whole `orchard start` command for minutes. Both
  deletes also pass `--wait=false` as a second, independent guard against that same hang class,
  regardless of ordering.

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
- **Default VM sizing (`CreateConfig.applyDefaults` in `internal/k8s/cluster.go`, mirrored in
  `ScaleConfig.applyDefaults` in `internal/k8s/scale.go` for new workers) is control plane 2
  vCPU/3072M, worker 4 vCPU/4096M each — control plane gets *less* memory than each worker,
  deliberately.** Measured live via `container stats` + a `/dev/shm` memory-stress test: the
  control plane's fixed overhead (etcd/apiserver/controller-manager/scheduler, plus CoreDNS and
  local-path-provisioner, which reliably land there since it's `Ready` before workers finish
  joining and their pods tolerate the control-plane taint) sits at ~1.6-1.8GB regardless of
  configured size and doesn't grow with workload size — 2048M left it dangerously tight
  (~85-90% utilized at idle), 3072M settled at a comfortable ~55-60%. Workers only need
  ~650-700MB for kubelet/kube-proxy/kindnet; everything past that is headroom for whatever gets
  deployed to test, which is the entire point of a local dev cluster. Don't "fix" this back
  toward equal or CP-heavy sizing without re-measuring — that was the bug being corrected.
  `scale.go`'s worker default must stay in sync with `create`'s worker default (both currently
  4096M) or `orchard scale` without explicit `--worker-memory` silently adds workers sized
  differently from the rest of the cluster; note that `scale` doesn't consult the cluster's
  saved `state.Cluster.WorkerMem` at all currently, only its own hardcoded default or an
  explicit flag — a pre-existing gap, not introduced by this sizing change.
