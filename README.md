# rook-mon-dns

A Kubernetes operator that watches the Rook Ceph `rook-ceph-mon-endpoints`
ConfigMap and publishes the monitor **node IPs** as DNS records via
[ExternalDNS](https://github.com/kubernetes-sigs/external-dns) `DNSEndpoint`
objects. This makes the Ceph monitors reachable by name for clients outside
the cluster (e.g. Proxmox, bare-metal hosts, VMs).

> [!WARNING]
> **Alpha – use at your own risk.** So far this operator has only been
> tested with unit and envtest tests, not yet on a real cluster. The API (`v1alpha1`) may change without a
> migration path. Test it in a non-critical environment first; no warranty
> of any kind (see the license).
>
> **AI-assisted:** Large parts of the code, tests and documentation were
> written with the help of AI coding assistants and reviewed by a human.
>
> This is an independent community project. It is not affiliated with or
> endorsed by the Rook project, Ceph or the CNCF.

## How it works

```mermaid
flowchart LR
    subgraph k8s["Kubernetes cluster"]
        rook["Rook operator"]
        cm[("ConfigMap<br/>rook-ceph-mon-endpoints")]
        cr[("CephMonDNSEndpoint<br/>(this CRD)")]
        op["rook-mon-dns<br/>operator"]
        dnsep[("DNSEndpoint<br/>externaldns.k8s.io")]
        edns["ExternalDNS"]
        mons["Ceph mons<br/>(host network on nodes)"]
    end
    dns[("DNS provider<br/>e.g. PowerDNS, RFC2136, Route53")]
    client["External Ceph clients<br/>Proxmox, bare metal, VMs"]

    rook -- "writes mon → node mapping" --> cm
    op -- "watches" --> cm
    op -- "watches, updates status, emits Events" --> cr
    op -- "creates/updates (owner ref)" --> dnsep
    edns -- "watches" --> dnsep
    edns -- "writes A/AAAA (+ optional SRV) records" --> dns
    client -- "resolves mon1…monN, mon-all, SRV (default names)" --> dns
    client -- "connects to mons on node IPs (msgr2 3300 / v1 6789)" --> mons
```

The operator only talks to the Kubernetes API server: it never contacts your
DNS provider or Ceph directly. ExternalDNS does the actual DNS update.

Rook stores the mon endpoints in the `rook-ceph-mon-endpoints` ConfigMap: the
`data` field lists each mon's IP and port, the `mapping` field the node each
mon runs on with its node IP. With `hostNetwork` both IPs are the same and
reachable from outside the cluster. The operator parses the mapping, sorts the
mons by ID and publishes:

| Record | Type | Content |
|---|---|---|
| `mon1.<zone>` … `monN.<zone>` | A/AAAA | one record per mon, sorted by mon ID |
| `mon-all.<zone>` | A/AAAA | all mon node IPs in one record (round-robin) |
| `_ceph-mon._tcp.<zone>` | SRV | optional, for Ceph client discovery via `mon dns srv name`; port per mon from the ConfigMap (6789 or 3300) |

When a mon is replaced, the numbering of the other mons can shift, so point
clients at `mon-all` or the SRV record (see [design notes](docs/design.md)).

The records are written into a single `DNSEndpoint` object per
`CephMonDNSEndpoint` resource; ExternalDNS picks it up from there.

## Prerequisites

1. **Rook Ceph cluster with `hostNetwork: true`** – without host networking
   the mons listen on ClusterIPs that are not reachable from outside. The
   operator detects this (`HostNetworkRequired`) and publishes nothing.
2. **DNSEndpoint CRD installed:**
   ```sh
   kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/external-dns/master/config/crd/standard/dnsendpoints.externaldns.k8s.io.yaml
   ```
3. **ExternalDNS running with `--source=crd`** and the TXT registry (so it
   owns and cleans up the records).

The operator needs no access to your DNS provider – ExternalDNS handles that.

## Installation

There is no release yet. Until `v0.1.0`, images are published as
`ghcr.io/rook-mon-dns/rook-mon-dns:main` (latest `main`) and
`ghcr.io/rook-mon-dns/rook-mon-dns:sha-<commit>` (pin this one), for amd64 and
arm64, built by CI from every commit on `main`. You can also
build your own image as shown below.

### Kustomize

```sh
# build & push the image
make docker-build docker-push IMG=<registry>/rook-mon-dns:<tag>

# deploys into the rook-ceph namespace, watching only that namespace
make deploy IMG=<registry>/rook-mon-dns:<tag>

# create the resource
kubectl apply -k config/samples/
```

Or generate a single installer file with
`make build-installer IMG=<registry>/rook-mon-dns:<tag>` and apply
`dist/install.yaml`.

### Helm

```sh
helm install rook-mon-dns charts/rook-mon-dns \
  --namespace rook-ceph \
  --set image.tag=sha-<commit>      # or image.repository=<registry>/rook-mon-dns
```

Interesting values (see `values.yaml`): `watchNamespaces` (one Role/RoleBinding
per namespace), `allowedZones`, `networkPolicy.*`, `metrics.enabled`.

## Configuration

```yaml
apiVersion: rook-mon-dns.github.io/v1alpha1
kind: CephMonDNSEndpoint
metadata:
  name: rook-ceph
  namespace: rook-ceph
spec:
  zone: ceph.example.com        # required; base zone for all records
  # recordTemplate: "mon{n}"    # {n}=sequence number, {id}=mon ID
  # allRecordName: "mon-all"    # "" disables the aggregate record
  # ttl: 300
  # srv:
  #   enabled: false            # _ceph-mon._tcp.<zone> SRV record
```

Operator flags: `--watch-namespaces` (restrict cache + RBAC scope),
`--allowed-zones` (restrict which `spec.zone` values are accepted).

The operator is read-only towards Rook and carries namespace-scoped RBAC only
(Roles, no ClusterRole at runtime). Deleting a `CephMonDNSEndpoint` removes
its `DNSEndpoint` (owner reference), and ExternalDNS removes the DNS records.

## Development

Requirements: Go (version in `go.mod`), make, Docker and
[kind](https://kind.sigs.k8s.io/) for the e2e tests. Other tools (controller-gen,
kustomize, envtest, golangci-lint) are downloaded into `bin/` by `make`. With
Nix, `nix-shell` provides everything.

```sh
make test               # unit + envtest integration tests
make test-e2e           # end-to-end tests in a throwaway kind cluster
make lint               # golangci-lint
make run                # run locally against your current kubeconfig
make manifests generate # regenerate CRDs/RBAC/deepcopy after type changes
```

## Status conditions

Except for `ZoneNotAllowed`, a `Ready=False` reason keeps the previously
published records, so a briefly broken ConfigMap does not take down mon DNS.

| Reason | Meaning |
|---|---|
| `Synced` | DNS records are in sync with the ConfigMap |
| `ConfigMapNotFound` | source ConfigMap missing (retries every minute) |
| `MappingEmpty` | the ConfigMap has no node mapping (mons not pinned to nodes) |
| `MappingInvalid` | mapping field could not be parsed |
| `HostNetworkRequired` | mon IPs differ from the node IPs: the mons are not on the host network |
| `RecordNameConflict` | two records would get the same name (e.g. `allRecordName: mon1`) |
| `DNSEndpointConflict` | a `DNSEndpoint` with the CR's name exists and belongs to someone else |
| `ZoneNotAllowed` | zone not in `--allowed-zones`; the records are removed |

## License

Copyright 2026 The rook-mon-dns Authors.

Licensed under the [Apache License, Version 2.0](LICENSE). Distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND.
