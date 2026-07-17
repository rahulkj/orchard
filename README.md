# orchard

Kubernetes clusters where every node is its own VM, grown on top of Apple's
native [`container`](https://github.com/apple/container) CLI.

Neither `apple/container` nor `minikube` have official Kubernetes support:
`apple/container` has an unmerged, unshipped [experimental proposal](https://github.com/apple/container/discussions/1673)
for a `k8s` plugin, and minikube has no `apple/container` driver ([open
issue](https://github.com/kubernetes/minikube/issues/20933), unimplemented).
`orchard` fills that gap directly: it drives `kubeadm` against node VMs
booted with `apple/container`, no third-party or unofficial dependency in
the loop.

## Install

```bash
brew install rahulkj/tap/orchard
```

Or build from source (see [Development](#development) below).

## Prerequisites

- macOS on Apple silicon, with [`apple/container`](https://github.com/apple/container) installed
- `kubectl`
- Go (version pinned in `go.mod`, only needed if building from source)

```bash
container system start                        # first-time init
container system kernel set --recommended     # first-time init, if prompted
orchard doctor
```

## Quick start

```bash
orchard create --name dev --workers 2
kubectl get nodes -o wide      # Kubernetes view
container list                 # apple/container view — same nodes, as VMs
```

## Commands

### create

```bash
orchard create --name dev --workers 2
```

Boots one control-plane VM and N worker VMs (each an `apple/container`
container running a `kindest/node` image with `kubeadm`, `kubelet`, and
`containerd` pre-installed), runs `kubeadm init`/`kubeadm join`, installs a
CNI, the bundled local-path storage class, and metrics-server. Merges an
`orchard-dev` context into `~/.kube/config` and switches to it.

Flags:

| Flag | Default | Notes |
|---|---|---|
| `--workers` | `2` | `0` gives a single untainted node |
| `--image` | pinned `kindest/node` digest | override to pin a different Kubernetes version |
| `--cp-cpus` / `--cp-memory` | `2` / `3072M` | control-plane VM sizing |
| `--worker-cpus` / `--worker-memory` | `4` / `4096M` | per-worker VM sizing |
| `--cni` | `kindnet` | see [CNI choice](#cni-choice) below |
| `--no-metrics` / `--no-storage` | off | skip those addons |
| `--headlamp` | off | install the [Headlamp](#headlamp) web UI |
| `--proxy-forward` | off | see [proxy-forward](#proxy-forward) below |

### scale

```bash
orchard scale --name dev --workers 4   # boots 2 more, joins them
orchard scale --name dev --workers 1   # drains + removes the highest-indexed workers
```

Scaling up reuses the lowest free worker indices and the cluster's original
`--proxy-forward` setting; scaling down removes the highest-indexed workers
first (`kubectl drain` + `kubectl delete node` + VM removal), so
lower-numbered workers stay stable across resizes.

### start / stop

```bash
orchard stop --name dev    # stops every node VM without deleting them
orchard start --name dev   # boots them back up
```

`stop` shuts down the cluster's node VMs in place, leaving them (and the
cluster's kubeconfig entries and saved state) untouched. `start` boots them
back up, then repairs the things a fresh DHCP lease on the node VMs would
otherwise break: it regenerates the API server's serving certificate for
its new IP, repoints admin.conf/super-admin.conf and each worker's
kubelet.conf at it, repoints the cluster-wide `kube-proxy` ConfigMap and
restarts kube-proxy and CoreDNS, and refreshes the merged kubeconfig entry
on the host. Without the `kube-proxy`/CoreDNS repair, Service ClusterIPs
(including the one CoreDNS itself depends on) silently stop routing after
any restart where the IP changed, leaving CoreDNS stuck `NotReady` and
anything that depends on cluster DNS or a Service IP breaking with it.

### delete

```bash
orchard delete --name dev
```

Removes every node VM, the cluster's kubeconfig entries, and its saved state.

### list / doctor

```bash
orchard list      # clusters and their nodes
orchard doctor     # checks apple/container, the system service, and kubectl
```

### check-updates / upgrade

```bash
orchard check-updates
orchard upgrade --name dev
```

`check-updates` compares each running cluster's Kubernetes version against
the newest `kindest/node` build published on Docker Hub. `upgrade` moves a
cluster to a newer (or explicitly `--image`-pinned) build.

**This is a destroy-and-recreate, not an in-place kubeadm upgrade.**
kubeadm only supports upgrading one minor version at a time using
kubeadm/kubelet binaries already present on the node, and these node images
are immutable, single-version builds with no general internet egress to
fetch different ones. Upstream `kind` has the same limitation and takes the
same approach — there's no in-place "kind upgrade" either. `orchard upgrade`
reads the cluster's saved settings (worker count, sizing, CNI, addons),
deletes it, and recreates it on the new image. **Workloads are not
preserved.** It prompts for confirmation unless run with `--yes`.

### self-update

```bash
orchard self-update
```

Checks GitHub releases for `rahulkj/orchard` (override with `--repo`) and
replaces the running binary with the latest one. Expects a goreleaser-style
release: an asset named `orchard_<os>_<arch>.tar.gz` containing an `orchard`
binary. Releases in that shape are published automatically by
`.github/workflows/release.yml` whenever a `v*` tag is pushed (see
[Releasing](#releasing) below). Before the first tag, this correctly
reports "no releases found" — that's expected, not a bug.

## CNI choice

```bash
orchard create --name dev --cni kindnet   # default, verified working
orchard create --name dev --cni flannel   # confirmed broken, see below
orchard create --name dev --cni calico    # untested
```

- **kindnet** (default): ships inside the node image, needs no internet
  access from the guest. The only CNI actually verified end-to-end here,
  including cross-node pod connectivity.
- **flannel**: confirmed broken, empirically, not by assumption. Its CNI
  config delegates pod interface setup to the standard CNI `bridge` plugin,
  which the `kindest/node` image does not ship (only kindnet's plugin
  binaries are present). Pods sit in `ContainerCreating` forever with
  `failed to find plugin "bridge" in path [/opt/cni/bin]`. Confirmed with a
  live 2-node cluster and cross-node pod scheduling.
- **calico**: untested. Its own CNI plugin wires pod interfaces itself
  (unlike flannel, no delegation to `bridge`), so it may not hit the same
  wall — but it hasn't been verified against this kernel/node image either.

## Headlamp

```bash
orchard create --name dev --headlamp        # install at cluster creation
orchard headlamp install --name dev         # or install into an existing cluster
orchard headlamp token --name dev           # print a fresh access token
```

Installs [Headlamp](https://github.com/kubernetes-sigs/headlamp) (Service +
Deployment in `kube-system`, via the upstream manifest) plus a
`headlamp-admin` ServiceAccount bound to `cluster-admin` — the upstream
manifest references that service account but doesn't create it, so orchard
does. `cluster-admin` is appropriate for a local throwaway dev cluster, not
a shared one.

**To access it** (verified end-to-end: install → token → port-forward →
`HTTP 200` from the actual UI):

```bash
kubectl port-forward -n kube-system service/headlamp 8080:80
open http://localhost:8080
```

Log in with the token `orchard headlamp token` prints (valid 1 year; rerun
the command any time to mint a new one).

## proxy-forward

```bash
orchard create --name dev --proxy-forward
```

Node VMs get their network from `apple/container`'s NAT and inherit
whatever the host's network path does to outbound traffic — on a managed
corporate Mac that commonly includes a **transparent TLS-intercepting
security agent** (Zscaler/Netskope-style), which is a different problem
than an explicit `HTTP_PROXY`: the guest needs the interception root CA
trusted, not a proxy pointed anywhere. `--proxy-forward` does two
independent things per node:

1. **Always**: exports certificates from the Mac's `System.keychain`
   (where MDM/security-agent root CAs get installed — deliberately not
   `SystemRootCertificates.keychain`, since a Linux guest already trusts
   Apple's ~150 default roots) and installs them into the guest via
   `update-ca-certificates`. Verified: certs land at
   `/usr/local/share/ca-certificates/host-proxy-cas.crt` inside the VM.
2. **If `HTTP_PROXY`/`HTTPS_PROXY` are set** on the host: forwards them into
   the guest's `/etc/environment` and a systemd drop-in for `containerd`,
   rewriting `127.0.0.1`/`localhost` to the guest's default gateway (since
   loopback inside the guest means the guest itself).

`scutil --proxy`/PAC auto-config is deliberately not consulted: on managed
Macs it commonly reports a PAC script served from a loopback port bound by
a local security agent, which a guest VM can't reach and which can't be
evaluated without a JS engine. `HTTP_PROXY`/`HTTPS_PROXY` env vars are the
portable mechanism this forwards.

## Design notes / gotchas

- **Node VMs have no general internet egress.** `apple/container` pulls
  images host-side, so `kindest/node` ships its core images pre-loaded and
  never needs the guest to reach the network itself. A manifest URL is
  different — `kubectl apply -f <url>` run *inside* the guest fails. Every
  addon that needs a URL (metrics-server, flannel/calico, Headlamp) is
  fetched **on the host** and piped into the guest via
  `container exec -i ... kubectl apply -f -`.
- **`kubeadm init` is pinned to `--kubernetes-version` read from the node
  image's own `/kind/version` file.** Without this, kubeadm's default
  "resolve latest stable from the internet" check is flaky here and can
  return a version newer than what's baked into the image, forcing a
  network image pull that fails with an untrusted-certificate error inside
  the guest.
- Default node image is a `kindest/node` build pinned by digest (currently
  `v1.36.1`), verified to boot, reach `Ready`, and pass cross-node pod
  connectivity under this `apple/container` version.
- **Node VMs get a fresh DHCP lease every boot, which breaks `kindest/node`'s
  own IP-fixup on `orchard start`.** The image's entrypoint tries to
  regenerate the API server's serving cert on an IP change via `kubeadm init
  phase certs apiserver --config /kind/kubeadm.conf` — a file only `kind`'s
  own tooling writes, never present here since `orchard create` runs
  `kubeadm init` with flags, not `--config`. That failure, under the
  entrypoint's `set -o errexit`, kills the whole boot outright (the VM ends
  up back in `stopped`, not just delayed) — `orchard start` retries the VM
  boot itself a few times to ride this out. The same fixup also only
  rewrites a node's references to its *own* address, never a worker's
  reference to the control plane's address, and never touches
  `admin.conf`/`super-admin.conf` at all. Separately, the cluster-wide
  `kube-proxy` ConfigMap bakes in the control plane's IP *at kubeadm-init
  time* and is never touched by anything on any restart, since it's a
  Kubernetes API object, not a node-local file a boot script could sed.
  Left stale, kube-proxy can't reach the API server after a restart at all,
  so it never programs a single Service iptables rule -- not just for
  `kubernetes`, for every ClusterIP in the cluster -- which is what
  actually leaves CoreDNS stuck `NotReady` forever (its `kubernetes` plugin
  talks to the API through the in-cluster ClusterIP). `orchard start`
  repairs all of this: regenerates the API server cert for the current IP,
  repoints `admin.conf`/`super-admin.conf` and every worker's
  `kubelet.conf` at it, restarts each worker's kubelet, repoints the
  `kube-proxy` ConfigMap and restarts kube-proxy, and restarts CoreDNS
  (whose in-process client latches onto its first, broken connection
  attempt and won't recover on its own even after kube-proxy is fixed). See
  `repairAPIServerCert`, `repointWorkerKubelet`, `repairKubeProxyConfig`,
  and `restartCoreDNS` in `internal/k8s/cluster.go`.
- **Default VM sizing puts more memory on workers than the control plane, not the other way
  around** — measured live on a 2-worker cluster via `container stats` and a `/dev/shm` memory
  stress test. At idle, the control plane's own components (etcd, apiserver,
  controller-manager, scheduler) plus whatever addon pods land there (CoreDNS and
  local-path-provisioner reliably schedule onto the control plane in practice, since it reaches
  `Ready` before workers finish joining and their pods tolerate the control-plane taint) used
  ~1.6-1.8GB regardless of which CP memory size was configured. 2048M left only ~200-400MB
  headroom (too tight once real API load adds up); 3072M settled at a comfortable ~55-60%
  utilized and stayed stable through 50 rapid-fire object creations. Workers, by contrast, only
  need ~650-700MB for kubelet/kube-proxy/kindnet at idle — the rest of whatever they're
  allocated is headroom for whatever you actually deploy to test. Verified a worker configured
  at 4096M can genuinely absorb a ~3GB memory workload without kubelet/containerd hiccuping. If
  you tune these further, don't shrink the control plane below ~2.5-3GB without re-measuring —
  addon-pod placement onto the control plane isn't something orchard's `Create` currently
  prevents.

## Development

```bash
make build   # go build, version stamped via ldflags
make check   # gofmt, go vet, golangci-lint, go test -race
```

`.github/workflows/ci.yml` runs the same checks on every push and pull
request against `main`.

## Releasing

Pushing a `v*` tag (e.g. `v0.1.0`) triggers
`.github/workflows/release.yml`, which runs
[GoReleaser](https://goreleaser.com) (`.goreleaser.yaml`) to build
`darwin/amd64` and `darwin/arm64` binaries, package them as
`orchard_<os>_<arch>.tar.gz`, publish them to a GitHub Release — the
exact shape `orchard self-update` expects — and push an updated cask to
[rahulkj/homebrew-tap](https://github.com/rahulkj/homebrew-tap).

The Homebrew push needs a token with write access to that separate repo
(the default `GITHUB_TOKEN` is scoped to this repo only). One-time setup:

1. Create a fine-grained personal access token scoped to just
   `rahulkj/homebrew-tap` with **Contents: read and write** permission
   (Settings → Developer settings → Personal access tokens → Fine-grained
   tokens).
2. In this repo, add it as an Actions secret named
   `HOMEBREW_TAP_GITHUB_TOKEN` (Settings → Secrets and variables →
   Actions).

## License

Apache License 2.0 — see [LICENSE](LICENSE).
