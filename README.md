# Midnightthoughts GitOps

GitOps configuration for Kubernetes on [Talos Linux](https://www.talos.dev/) using [Flux CD](https://fluxcd.io/).

## Stack

| Component | Technology |
|---|---|
| OS | [Talos Linux](https://www.talos.dev/) |
| GitOps | [Flux CD](https://fluxcd.io/) |
| Cloud | [Hetzner Cloud](https://www.hetzner.com/cloud) (CAX nodes) + Hetzner LBs |
| CNI | [Cilium](https://cilium.io/) |
| Ingress | [Envoy Gateway](https://gateway.envoyproxy.io/) |
| Storage | [Longhorn](https://longhorn.io/) (block) + Hetzner Object Storage (S3) |
| Database | [CloudNativePG](https://cloudnative-pg.io/) (PostgreSQL 18) |
| Secrets | [SOPS](https://getsops.io/) + [age](https://age-encryption.org/) |
| Backups | [Velero](https://velero.io/) (4× daily) |
| Monitoring | VictoriaMetrics + Grafana + Loki |
| Auth | [Authentik](https://goauthentik.io/) |
| Updates | [Renovate](https://docs.renovatebot.com/) |
| TLS | [cert-manager](https://cert-manager.io/) + Let's Encrypt |

## Repository Structure

```
├── clusters/talos_cluster/     # Flux bootstrap & Kustomizations
├── infrastructure_talos/       # Controllers (CNI, storage, cert-manager, monitoring)
│   ├── controllers/            # Longhorn, Velero, CloudNativePG, etc.
│   └── configs/               # Cluster config (CNPG cluster, issuers, secrets)
├── apps/
│   └── talos_cluster/         # All deployed applications
├── .github/workflows/         # CI: validation, security scans, docs deployment
└── scripts/                   # Helper scripts
```

## Quick Start

```bash
# Generate Talos config
talosctl gen config cluster-2025 https://CONTROL_PLANE_IP:6443
talosctl apply-config --insecure --nodes CONTROL_PLANE_IP --file controlplane.yaml
talosctl bootstrap --nodes CONTROL_PLANE_IP
talosctl kubeconfig --nodes CONTROL_PLANE_IP

# Install Flux
flux check --pre
kubectl apply -k clusters/talos_cluster/flux-system

# Setup SOPS (generate a NEW key — never reuse an existing one)
age-keygen -o age.key
kubectl create secret generic sops-age --namespace=flux-system --from-file=age.agekey=age.key
# Update .sops.yaml with your public key
# Store age.key somewhere secure (password manager, not in this repo)

# Trigger reconciliation
flux reconcile kustomization flux-system --with-source
```

## Working with Secrets

```bash
# Encrypt a new secret
sops -e -i secret.yaml

# Edit an encrypted secret
sops secret.yaml

# View decrypted (don't commit output)
sops -d secret.yaml
```

The `encrypted_regex` in `.sops.yaml` controls which fields are encrypted. All secrets are encrypted with age using a shared cluster key stored in the `sops-age` Kubernetes secret.

## Common Operations

```bash
# Force reconcile after a push
flux reconcile kustomization flux-system --with-source

# Check status of all Flux resources
flux get all -A

# Show recent Flux errors
flux logs --level=error --all-namespaces

# Rollback: revert the commit and push
git revert HEAD && git push
```

## Backups

Velero runs 4× daily (00:00, 06:00, 12:00, 18:00 UTC) backing up all cluster resources and Longhorn volumes to Hetzner Object Storage. CloudNativePG WAL archiving provides continuous PostgreSQL backup to a separate S3 bucket.

```bash
# Check backup status
kubectl get backup.velero.io -n velero --sort-by='.metadata.creationTimestamp'

# Trigger manual backup
velero backup create manual-$(date +%Y%m%d-%H%M) --include-namespaces '*'
```

## CI/CD

| Workflow | Trigger | Purpose |
|---|---|---|
| `validate.yaml` | PR / push to main | Manifest validation, security scans (gitleaks, trivy, kubescape) |
| `docs.yml` | Push to main (*.md changes) | Build and deploy MkDocs to GitHub Pages |
| `build-continuwuity.yaml` | Manual | Custom Continuwuity image build |

Run validation locally:
```bash
./scripts/validate.sh
```

## Documentation

```bash
# Install doc dependencies (once)
make docs-install

# Preview locally at http://127.0.0.1:8000
make docs

# Build to verify (outputs to site/)
make docs-build
```

Add a `README.md` to any app directory under `apps/talos_cluster/<app>/` and it will automatically appear in the docs navigation.

## Troubleshooting

```bash
# Flux
flux check
flux logs --all-namespaces

# App not reconciling
kubectl describe kustomization <name> -n flux-system
kubectl describe helmrelease <name> -n <namespace>

# Check pod
kubectl describe pod <pod> -n <namespace>
kubectl logs <pod> -n <namespace> --previous

# Talos node health
talosctl health --nodes <NODE_IP>
talosctl logs -n <NODE_IP>
```
