# Plan: rook-mon-dns

A Kubernetes operator that reads the Rook Ceph monitor endpoints from the
`rook-ceph-mon-endpoints` ConfigMap and publishes them as DNS records via
ExternalDNS.

---

## 0. Implementation status

**Complete (as of 2026-08-13).** All implementation steps done and verified:

- [x] Bootstrap (kubebuilder init/create api, group `cephmons.github.io/v1alpha1`)
- [x] CRD types per §5 (CEL validation, defaults, print columns)
- [x] DNSEndpoint types in `internal/externaldns/v1alpha1` (mirrored from the
      official external-dns CRD; needs `+kubebuilder:object:generate=true` at
      package level or controller-gen only covers root types)
- [x] ConfigMap parser + 10 unit tests (real CM from §2 as fixture)
- [x] DNSEndpoint builder + 6 unit tests (templates, dedupe, SRV, TTL)
- [x] Reconciler (watches, guardrails, status/conditions/events, rejection
      deletes stale DNSEndpoint) + 8 fake-client tests
- [x] envtest integration tests (8 Ginkgo specs); external-dns CRD in `test/crds/`
- [x] Hardening per §10: PSS-restricted manager, no CPU limit, Role-based RBAC
      (`config/rbac/watched_role*.yaml`; the generated ClusterRole in
      `role.yaml` is kept for reference but NOT applied), NetworkPolicies
      (metrics/probes/egress), `--watch-namespaces` + `--allowed-zones`,
      DNSEndpoint CRD startup check
- [x] Kustomize sample CR + `make build-installer` → `dist/install.yaml`,
      kubeconform strict 17/17 valid (k8s 1.30)
- [x] Helm chart `charts/rook-mon-dns` (CRD in `crds/`,
      per-namespace Roles from `watchNamespaces`), helm lint + kubeconform 8/8
- [x] `make test` green (24 unit + 8 envtest, controller coverage 83.3 %),
      `make docker-build` green (distroless image)
- [x] README.md, shell.nix (dev toolchain)

**Gotchas encountered (for future reference):**
- Optional CRD fields with defaults need `omitempty`/`omitzero` in the Go JSON
  tags, otherwise the Go client sends `""`/`0` explicitly and the API server
  never applies the default (found via envtest).
- Go module/build caches must NOT live under the project root:
  controller-gen's `./...` resolution descends into them and breaks
  (they live in `/tmp/opencode` via shell.nix).
- kubeconform 0.7.0's `-schema-location default` cannot resolve the
  CustomResourceDefinition schema; use the explicit kubernetes-json-schema
  URL (see §10 validation plan).
- envtest: `suite_test.go` picks the alphabetically first dir in `bin/k8s/` –
  avoid multiple envtest versions there during downloads.

**Toolchain:** everything runs via `nix-shell` (see AGENTS.md). No system-wide
Go/kubebuilder installation needed.

**Remaining (deliberately out of scope for v1):**
- e2e tests against a real cluster (`test/e2e/` scaffold exists, needs kind)
- CI/CD (decision: local builds only)
- Publishing images to a real registry + Helm chart hosting

---

## 1. Decisions (from the interview)

| Topic | Decision |
|---|---|
| Target IPs | Node IPs from the `mapping` field of the ConfigMap (no ClusterIPs) |
| ExternalDNS integration | Via the `DNSEndpoint` CRD (`externaldns.k8s.io/v1alpha1`) |
| Tech stack | Go + Kubebuilder (controller-runtime) |
| Configuration | Custom CRD, one CR instance per Ceph cluster |
| DNS scheme | Stable names `mon1`…`monN` (sorted by mon ID) + aggregate record `mon-all`; names built from zone + template |
| TTL | Default 300s, configurable in the CR |
| SRV records | Yes, optional via spec flag (`_ceph-mon._tcp.<zone>`, port 6789) |
| API group | Neutral public group: `cephmons.github.io/v1alpha1` (placeholder – adjust to your own GitHub org/domain) |
| Deployment | Kustomize (Kubebuilder standard) + Helm chart |
| CI/CD | No setup, local builds only (`make`, `docker-build`) |

**Note on the API group:** `*.k8s.io` is reserved for official Kubernetes
projects and must not be used. Common practice for open-source operators is
`<project>.github.io` or your own domain.

---

## 2. Background: data source

The `rook-ceph-mon-endpoints` ConfigMap (namespace `rook-ceph`) contains:

```yaml
data:
  # ClusterIPs – reachable ONLY inside the cluster, NOT used:
  data: g=10.109.56.143:6789,l=10.97.138.166:6789,j=10.97.18.232:6789
  # Node mapping – THIS is our source:
  mapping: '{"node":{"g":{"Name":"talos-node-01","Hostname":"talos-node-01","Address":"192.168.1.103"},
                       "j":{"Name":"talos-node-03","...":"192.168.1.107"},
                       "l":{"Name":"talos-node-02","...":"192.168.1.105"}}}'
```

**Important:** The mon IDs (`g`, `j`, `l`) are **not stable** – Rook assigns
them sequentially (`maxMonId: 11`), and they change on mon failover. Therefore
the IDs are **sorted alphabetically** after parsing and mapped to stable names:

| Stable name | Mon ID | Node IP |
|---|---|---|
| `mon1` | g | 192.168.1.103 |
| `mon2` | j | 192.168.1.107 |
| `mon3` | l | 192.168.1.105 |

The mon port (6789) is parsed from the `data` string; default: 6789.

**Edge case:** If `mapping` is empty (e.g. Rook without `hostNetwork`), the
operator sets a `Ready=False` condition (`Reason: MappingEmpty`) instead of
silently publishing ClusterIPs.

---

## 3. Prerequisites (cluster-side)

1. **DNSEndpoint CRD installed:**
   `kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/external-dns/master/config/crd/standard.yaml`
2. **ExternalDNS running with `--source=crd`** (user's own deployment).
3. ExternalDNS configured with the **TXT registry**, so it takes ownership of
   the managed records.
4. Rook Ceph cluster with `hostNetwork: true` (otherwise `mapping` is empty).

The operator itself needs **no** access to the DNS provider.

---

## 4. Architecture

```
┌─────────────────────────┐         ┌──────────────────────────────┐
│ ConfigMap               │  watch  │ Operator                     │
│ rook-ceph-mon-endpoints │────────▶│                              │
└─────────────────────────┘         │  1. read CM & parse mapping  │
                                    │  2. sort → mon1..monN        │
┌─────────────────────────┐         │  3. build/update DNSEndpoint │
│ CephMonDNSEndpoint (CR) │────────▶│  4. set status/conditions    │
└─────────────────────────┘         └──────────────┬───────────────┘
                                                   │ create/update
                                                   ▼
                                    ┌──────────────────────────────┐
                                    │ DNSEndpoint (externaldns…)   │
                                    └──────────────┬───────────────┘
                                                   │ watch (--source=crd)
                                                   ▼
                                    ┌──────────────────────────────┐
                                    │ ExternalDNS → DNS provider   │
                                    └──────────────────────────────┘
```

**One DNSEndpoint object per CR** (atomic update: all records are replaced in
one update – no stale entries when a mon disappears).
An OwnerReference from the CR to the DNSEndpoint provides garbage collection
on deletion.

The `DNSEndpoint` Go types are **re-implemented as a minimal internal API**
(the external-dns repo is not intended to be used as a library); the
v1alpha1 API is small and stable.

---

## 5. CRD design

```yaml
apiVersion: cephmons.github.io/v1alpha1
kind: CephMonDNSEndpoint               # scope: Namespaced
metadata:
  name: rook-ceph
  namespace: rook-ceph
spec:
  # Source
  configMapRef:
    name: rook-ceph-mon-endpoints   # default
    namespace: rook-ceph            # default: namespace of the CR; MUST equal it (v1)
  # DNS target
  zone: ceph.example.com            # base zone (required; must match --allowed-zones)
  recordTemplate: "mon{n}"          # placeholders: {n}=sequence number, {id}=mon ID
  allRecordName: "mon-all"          # aggregate record with all IPs; "" = disabled
  ttl: 300                          # default 300
  srv:
    enabled: true                   # default false
    name: "_ceph-mon._tcp"          # default
  targetNamespace: ""               # where DNSEndpoints land; MUST be empty or equal to the CR namespace (v1)
status:
  observedGeneration: 1
  publishedEndpoints:
    - dnsName: mon1.ceph.example.com
      monId: g
      target: 192.168.1.103
  conditions:
    - type: Ready
      status: "True"
      reason: Synced
```

**Resulting records** (example above):

| DNSName | Type | Target(s) | TTL |
|---|---|---|---|
| mon1.ceph.example.com | A | 192.168.1.103 | 300 |
| mon2.ceph.example.com | A | 192.168.1.107 | 300 |
| mon3.ceph.example.com | A | 192.168.1.105 | 300 |
| mon-all.ceph.example.com | A | 192.168.1.103, .107, .105 | 300 |
| _ceph-mon._tcp.ceph.example.com | SRV | `0 10 6789 mon1.…` (×3) | 300 |

The SRV records enable Ceph client discovery via `mon dns srv name`
in `ceph.conf`.

---

## 6. Reconciler logic

1. Load the CR (NotFound → done).
2. Load the ConfigMap according to `configMapRef` → error = condition `ConfigMapNotFound`, requeue with backoff.
3. Parse `mapping` (JSON) → list of (MonID, NodeName, Address).
   - Empty → condition `MappingEmpty`, **no** DNS update.
4. Sort by MonID; dedupe IPs for `mon-all`.
5. Parse the mon port from the `data` string (fallback 6789).
6. Compute the desired DNSEndpoint object (deterministic!).
7. `CreateOrUpdate` the DNSEndpoint in `targetNamespace`; set OwnerReference.
8. Update status (`publishedEndpoints`, conditions), emit a `Synced` event.

**Watches:**
- Primary: `CephMonDNSEndpoint`
- Secondary: `ConfigMap` (MapFunc: match CM name/namespace against the `configMapRef` of all CRs → enqueue)
- Secondary: `DNSEndpoint` (Owns, in case someone manually modifies/deletes the object)
- Periodic resync (e.g. 10 min) as a safety net.

**RBAC (least privilege, namespace-scoped – see §10):**
- `configmaps`: get, list, watch (Role in each watched namespace)
- `dnsendpoints.externaldns.k8s.io`: get, list, watch, create, update, patch, delete (Role in each watched namespace)
- own CRs + `/status` + `/finalizers`: standard (Role in each watched namespace)
- `events`: create, patch (Role in each watched namespace)
- `leases.coordination.k8s.io`: full CRUD (Role in the operator namespace, leader election)
- **No ClusterRoleBinding at runtime.** The only cluster-scoped object is the CRD itself, installed at deploy time.

**Security guardrails enforced by the reconciler (v1):**
- `configMapRef.namespace` and `targetNamespace` must be empty or equal to the CR's
  namespace → otherwise reject with condition `CrossNamespaceRefDenied`.
  (CRD CEL cannot access `metadata.namespace`, so this must live in the controller;
  an optional ValidatingAdmissionPolicy can enforce it at admission time, K8s ≥ 1.30.)
- `spec.zone` must match the operator's `--allowed-zones` list (if configured) →
  otherwise reject with condition `ZoneNotAllowed`.

---

## 7. Project structure

```
├── cmd/main.go
├── api/v1alpha1/
│   ├── cephmondnsendpoint_types.go
│   ├── groupversion_info.go
│   └── zz_generated.deepcopy.go        # generated
├── internal/
│   ├── externaldns/
│   │   └── v1alpha1/                   # own DNSEndpoint types + scheme
│   └── controller/
│       ├── cephmondnsendpoint_controller.go
│       ├── configmap_parser.go         # parse mapping/data
│       ├── dnsendpoint_builder.go      # sorting, template, SRV
│       └── *_test.go
├── config/                             # Kustomize (Kubebuilder standard)
│   ├── crd/bases/
│   ├── rbac/
│   ├── manager/
│   ├── default/
│   └── samples/
├── charts/rook-mon-dns/
├── Dockerfile                          # distroless, Kubebuilder standard
├── Makefile
├── PROJECT
└── README.md
```

---

## 8. Implementation steps

1. **Bootstrap:** `git init`; `kubebuilder init --domain github.io --repo <module>`;
   `kubebuilder create api --group cephmons --version v1alpha1 --kind CephMonDNSEndpoint --resource --controller`
2. **CRD types** as per section 5 (`+kubebuilder` validations, defaults), `make manifests generate`.
3. **DNSEndpoint types** under `internal/externaldns/v1alpha1` + register with the scheme.
4. **Parser** (`configmap_parser.go`) + unit tests (including your real CM example as a fixture; edge cases: empty mapping, missing keys, broken JSON).
5. **Builder** (`dnsendpoint_builder.go`) + unit tests (sorting, template `{n}`/`{id}`, dedupe, SRV format `0 10 6789 <fqdn>`).
6. **Reconciler** including watches, status/conditions, events, RBAC markers –
   plus the v1 security guardrails: cross-namespace rejection and `--allowed-zones`
   enforcement (see §10).
7. **envtest integration tests:** create CM → DNSEndpoint appears with correct records; change CM → update; empty CM → condition, no update; cross-namespace ref → rejected.
8. **Operator hardening** (see §10): manager Deployment with full securityContext,
   resource requests/limits, probes; namespace-scoped cache (`--watch-namespaces`);
   Role-based RBAC manifests; NetworkPolicy; startup check for the DNSEndpoint CRD.
9. **Kustomize:** `config/samples` with an example CR; `make build-installer`.
10. **Helm chart** under `charts/` (CRD in `crds/`, values: image, replicaCount, rbac, watchNamespaces, allowedZones, networkPolicy.enabled, …).
11. **Build:** `make docker-build IMG=…`; README with prerequisites (section 3), install and example YAML.

---

## 9. Test strategy

| Level | Tooling | Content |
|---|---|---|
| Unit | go test | Parser, builder (sorting, templates, edge cases) |
| Integration | envtest (Ginkgo) | End-to-end reconcile against a fake API server |
| Manual | kind cluster | With a real CM copy + DNSEndpoint CRD (ExternalDNS optionally mocked) |

---

## 10. Security & failure-mode hardening (KubeShark workflow results)

Execution context and assumptions (stated explicitly where unknown):

- **Distribution:** Talos Linux, bare metal (nodes `talos-node-01..03`, 192.168.1.0/24) – vanilla Kubernetes, no cloud provider.
- **Cluster version floor: 1.30** (CEL CRD validations, ValidatingAdmissionPolicy stable, current `policy/v1`, `autoscaling/v2`, …).
- **CNI:** unknown. If it is Talos' default Flannel, NetworkPolicies are **not enforced** – the NetworkPolicy below then only documents intent. Verify: Cilium/Calico enforce, Flannel does not.
- **Policy enforcement:** unknown → the operator is designed to pass **PSS `restricted`**.
- **Criticality:** single-cluster, prod-ish homelab → hardening worthwhile, but KISS: 1 replica, no PDB needed.
- **Add-ons:** Rook (hostNetwork), ExternalDNS (`--source=crd`). No service mesh, no GitOps assumed.

### Selected failure modes and remediations

| # | Failure mode | Risk for this operator | Remediation |
|---|---|---|---|
| 1 | **Privilege sprawl** (primary) | Operator can write DNS records via ExternalDNS → a compromised or over-privileged operator = DNS hijacking. CR creators could abuse the operator as a confusable deputy: read CMs in foreign namespaces, publish records for foreign zones. | Namespaced CRD; same-namespace constraint for `configMapRef`/`targetNamespace` (reconciler-enforced); **Role-only RBAC** per watched namespace, zero ClusterRoleBindings; `--allowed-zones` allowlist; dedicated SA; no secret access at all. |
| 2 | **Insecure workload defaults** | Kubebuilder scaffold ships without full securityContext. | PSS-restricted-compliant Deployment (see below); distroless image; namespace PSA labels. |
| 3 | **Resource starvation** | Scaffold comments out `resources` → BestEffort QoS. | Explicit small requests/limits (see below). No CPU limit (CFS throttling), memory limit mandatory. |
| 4 | **Network exposure** | Metrics `:8080` and probes `:8081` reachable cluster-wide; flat networking default. | NetworkPolicy: ingress only probes (node CIDR) + metrics (monitoring ns), egress only API server (+DNS). Caveat: needs enforcing CNI (see assumptions). |
| 5 | **Fragile rollouts** | Mutable image tag; double-active controllers during rollout. | Immutable tags/digests; leader election (Lease) enabled (Kubebuilder default) → RollingUpdate safe; `revisionHistoryLimit: 5`. |
| 6 | **API drift** | Hard dependency on `DNSEndpoint` CRD; hand-replicated v1alpha1 types can drift from external-dns. | Startup check: discovery lookup for `externaldns.k8s.io/v1alpha1`; fail fast with an actionable error if absent. kubeconform validation incl. CRDs-catalog schema for DNSEndpoint. Only stable APIs (`apiextensions/v1`, `policy/v1`, …). |

### Artifacts (design targets for step 8 of the implementation plan)

Manager Deployment – pod/container security (PSS `restricted`):

```yaml
spec:
  replicas: 1
  revisionHistoryLimit: 5
  template:
    spec:
      serviceAccountName: controller-manager        # dedicated SA (scaffold default)
      securityContext:
        runAsNonRoot: true
        seccompProfile: { type: RuntimeDefault }
      containers:
        - name: manager
          image: <registry>/rook-mon-dns:vX.Y.Z@sha256:…   # immutable
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: { drop: ["ALL"] }
          resources:
            requests: { cpu: 10m, memory: 64Mi }
            limits:   { memory: 128Mi }             # no CPU limit → no CFS throttling
          # probes from the scaffold: /healthz (liveness) + /readyz (readiness) on :8081
```

Role in each watched namespace (example `rook-ceph`) – no ClusterRole at runtime:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: { name: cephmon-publisher, namespace: rook-ceph }
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["externaldns.k8s.io"]
    resources: ["dnsendpoints"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["cephmons.github.io"]
    resources: ["cephmondnsendpoints"]
    verbs: ["get", "list", "watch", "update", "patch"]   # + create/delete if users delegate CR mgmt
  - apiGroups: ["cephmons.github.io"]
    resources: ["cephmondnsendpoints/status"]
    verbs: ["get", "update", "patch"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]
# Plus in the operator namespace: Role for leases.coordination.k8s.io (leader election).
```

NetworkPolicy (operator namespace; requires an enforcing CNI):

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: { name: controller-manager, namespace: cephmons-system }
spec:
  podSelector:
    matchLabels: { control-plane: controller-manager }
  policyTypes: [Ingress, Egress]
  ingress:
    # kubelet probes (health endpoints are unauthenticated by design)
    - from: [{ ipBlock: { cidr: 192.168.1.0/24 } }]     # node CIDR
      ports: [{ protocol: TCP, port: 8081 }]
    # metrics scraping from the monitoring stack only
    - from:
        - namespaceSelector:
            matchLabels: { kubernetes.io/metadata.name: monitoring }
      ports: [{ protocol: TCP, port: 8080 }]
  egress:
    - to: [{ ipBlock: { cidr: 10.96.0.0/12 } }]          # service CIDR → kube-apiserver
      ports: [{ protocol: TCP, port: 443 }]
    - to:                                                  # DNS (safety net)
        - namespaceSelector:
            matchLabels: { kubernetes.io/metadata.name: kube-system }
      ports: [{ protocol: UDP, port: 53 }, { protocol: TCP, port: 53 }]
```

Optional admission-time enforcement of the same-namespace rule (K8s ≥ 1.30, no webhook needed):

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata: { name: cephmondnsendpoint-same-namespace }
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: ["cephmons.github.io"]
        apiVersions: ["v1alpha1"]
        resources: ["cephmondnsendpoints"]
        operations: ["CREATE", "UPDATE"]
  validations:
    - expression: >-
        !has(object.spec.configMapRef.namespace) || object.spec.configMapRef.namespace == '' ||
        object.spec.configMapRef.namespace == object.metadata.namespace
      message: "spec.configMapRef.namespace must equal the CR namespace"
    - expression: >-
        !has(object.spec.targetNamespace) || object.spec.targetNamespace == '' ||
        object.spec.targetNamespace == object.metadata.namespace
      message: "spec.targetNamespace must equal the CR namespace"
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata: { name: cephmondnsendpoint-same-namespace }
spec:
  policyName: cephmondnsendpoint-same-namespace
  validationActions: [Deny]
```

### Validation plan (before any apply)

```bash
# schema validation (incl. external-dns DNSEndpoint CRD schema from CRDs-catalog)
# NOTE: use explicit schema locations; kubeconform's "default" keyword fails to
# resolve the CustomResourceDefinition schema itself.
kustomize build config/default | kubeconform -strict -kubernetes-version 1.30.0 \
  -schema-location 'https://raw.githubusercontent.com/yannh/kubernetes-json-schema/master/{{.NormalizedKubernetesVersion}}/{{.ResourceKind}}{{.KindSuffix}}.json' \
  -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'
helm template publisher charts/rook-mon-dns | kubeconform -strict -kubernetes-version 1.30.0 \
  -schema-location 'https://raw.githubusercontent.com/yannh/kubernetes-json-schema/master/{{.NormalizedKubernetesVersion}}/{{.ResourceKind}}{{.KindSuffix}}.json'

# server-side dry-run + diff review (never apply to prod without a reviewed diff)
kubectl apply --dry-run=server -k config/default
kubectl diff -k config/default

# RBAC audit: positive + negative checks against the operator SA
kubectl auth can-i --list --as=system:serviceaccount:cephmons-system:controller-manager -n rook-ceph
kubectl auth can-i get secrets --as=system:serviceaccount:cephmons-system:controller-manager -n rook-ceph   # must be "no"
kubectl auth can-i list configmaps --as=system:serviceaccount:cephmons-system:controller-manager -n default # must be "no"

# PSS audit: enforce restricted on the operator namespace, expect no violations
kubectl label ns cephmons-system pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/audit=restricted pod-security.kubernetes.io/warn=restricted

# e2e in kind: (a) start without DNSEndpoint CRD → operator exits with actionable error;
# (b) install CRD + sample CR → DNSEndpoint matches expected records;
# (c) delete CR → DNSEndpoint garbage-collected; (d) cross-namespace ref → condition set, no DNSEndpoint.
```

### Rollback / recovery notes

- **Operator rollout:** `kubectl rollout undo deploy/controller-manager -n cephmons-system` (history: 5 revisions, immutable tags make this deterministic).
- **DNS records:** records are owned by ExternalDNS via its TXT registry. Deleting a CR garbage-collects its DNSEndpoint (OwnerReference) → ExternalDNS removes the records. If records linger (registry misconfiguration), delete the DNSEndpoint manually and clean stale A/SRV/TXT records at the provider.
- **CRD upgrade:** v1alpha1 changes must stay backward-compatible within the version; breaking changes require v1alpha2 (no conversion webhook in v1).
- **Data safety:** the operator is read-only towards Rook (never writes the source ConfigMap) and stores no state besides DNSEndpoints + CR status – uninstall is non-destructive to Ceph.

---

## 11. Assumptions & open points

- **API group** `cephmons.github.io` is a placeholder – adjust it to your GitHub org/domain during bootstrap.
- No fallback to ClusterIPs when `mapping` is empty (deliberate decision, see §2).
- The operator manages exactly **one** ConfigMap format (current Rook versions). Older Rook versions without the `mapping` field are not supported.
- **v1 is single-namespace per watched namespace** (same-namespace constraint, §10). Multi-namespace support = one Role/RoleBinding per additional watched namespace (`--watch-namespaces`). Cross-namespace references are deliberately rejected.
- **CNI:** if Talos runs its default Flannel, NetworkPolicies are not enforced (see §10) – decide whether to switch to/keep Cilium.
- The project directory name currently contains the typo „publischer“ – consider renaming it to „publisher“ before creating the repo.
