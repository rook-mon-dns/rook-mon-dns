# rook-mon-dns

A Kubernetes operator that watches the Rook Ceph `rook-ceph-mon-endpoints`
ConfigMap and publishes the monitor **node IPs** as DNS records via
[ExternalDNS](https://github.com/kubernetes-sigs/external-dns) `DNSEndpoint`
objects. This makes the Ceph monitors reachable by name for clients outside
the cluster (e.g. Proxmox, bare-metal hosts, VMs).

## How it works

Rook stores the mon endpoints in the `rook-ceph-mon-endpoints` ConfigMap. The
`data` field contains ClusterIPs (only usable inside the cluster), while the
`mapping` field contains the mon-to-node assignment with real node IPs. The
operator parses the mapping, sorts the mons by ID (Rook mon IDs change on
failover, so the generated names stay stable) and publishes:

| Record | Type | Content |
|---|---|---|
| `mon1.<zone>` … `monN.<zone>` | A | one record per mon, sorted by mon ID |
| `mon-all.<zone>` | A | all mon node IPs in one record (round-robin) |
| `_ceph-mon._tcp.<zone>` | SRV | optional, for Ceph client discovery via `mon dns srv name` |

The records are written into a single `DNSEndpoint` object per
`CephMonDNSEndpoint` resource; ExternalDNS picks it up from there.

## Prerequisites

1. **Rook Ceph cluster with `hostNetwork: true`** – without host networking
   the ConfigMap's `mapping` field is empty and there are no node IPs to
   publish (the operator refuses to fall back to ClusterIPs).
2. **DNSEndpoint CRD installed:**
   ```sh
   kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/external-dns/master/config/crd/standard/dnsendpoints.externaldns.k8s.io.yaml
   ```
3. **ExternalDNS running with `--source=crd`** and the TXT registry (so it
   owns and cleans up the records).

The operator needs no access to your DNS provider – ExternalDNS handles that.

## Installation

### Kustomize

```sh
# build & push the image
make docker-build docker-push IMG=<registry>/rook-mon-dns:<tag>

# deploys into the rook-ceph namespace, watching only that namespace
make deploy IMG=<registry>/rook-mon-dns:<tag>

# create the resource
kubectl apply -k config/samples/
```

Or use the prebuilt installer from `dist/install.yaml`
(`make build-installer IMG=...`).

### Helm

```sh
helm install publisher charts/rook-mon-dns \
  --namespace rook-ceph \
  --set image.repository=<registry>/rook-mon-dns \
  --set image.tag=<tag>
```

Interesting values (see `values.yaml`): `watchNamespaces` (one Role/RoleBinding
per namespace), `allowedZones`, `networkPolicy.*`, `metrics.enabled`.

## Configuration

```yaml
apiVersion: cephmons.github.io/v1alpha1
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

The toolchain is provided by `shell.nix` (go, kubebuilder, kustomize,
kubeconform, kubectl, helm, make):

```sh
nix-shell

make test              # unit + envtest integration tests
make run               # run locally against your current kubeconfig
make manifests generate # regenerate CRDs/RBAC/deepcopy after type changes
```

After a reboot (Go caches live in `/tmp`): just `nix-shell --run 'go mod download'`.

## Status conditions

| Reason | Meaning |
|---|---|
| `Synced` | DNS records are in sync with the ConfigMap |
| `ConfigMapNotFound` | source ConfigMap missing (requeues) |
| `MappingEmpty` | no node mapping – cluster without `hostNetwork`? |
| `MappingInvalid` | mapping field could not be parsed |
| `CrossNamespaceRefDenied` | references must stay in the CR namespace |
| `ZoneNotAllowed` | zone not in `--allowed-zones` |
