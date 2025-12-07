# Talos Kubernetes GitOps Repository

GitOps configuration for Kubernetes on [Talos Linux](https://www.talos.dev/) using [Flux CD](https://fluxcd.io/).

## Stack

- **OS**: Talos Linux
- **GitOps**: Flux CD
- **Secrets**: SOPS + age
- **CNI**: Cilium
- **Updates**: Renovate

## Structure

```
├── clusters/talos_cluster/     # Flux bootstrap
├── infrastructure_talos/       # Controllers, monitoring
├── apps/                       # Applications
└── .github/workflows/          # CI (validation, renovate)
```

## Quick Start

```bash
# Bootstrap Talos
talosctl gen config my-cluster https://CONTROL_PLANE_IP:6443
talosctl apply-config --insecure --nodes CONTROL_PLANE_IP --file controlplane.yaml
talosctl bootstrap --nodes CONTROL_PLANE_IP
talosctl kubeconfig --nodes CONTROL_PLANE_IP

# Install Flux
flux check --pre
kubectl apply -k clusters/talos_cluster/flux-system

# Setup SOPS (generate NEW key, never use the one in repo!)
age-keygen -o age.key
kubectl create secret generic sops-age --namespace=flux-system --from-file=age.agekey=age.key
# Update .sops.yaml with your public key, store age.key securely

# Deploy
flux reconcile kustomization flux-system --with-source
```

## Secrets

```bash
# Encrypt
sops --encrypt --encrypted-regex '^(data|stringData)$' secret.yaml > secret.enc.yaml

# Edit
sops secret.enc.yaml
```

**Never commit:** `age.key`, `age.agekey`, decrypted secrets

## Common Tasks

```bash
# Deploy changes
git commit -am "update" && git push

# Force reconcile
flux reconcile kustomization flux-system --with-source

# Check status
flux get all -A
flux logs --level=error

# Rollback
git revert COMMIT && git push
```

## CI/CD

- **validate.yaml**: Validates manifests, runs security scans (gitleaks, trivy, kubeaudit)
- **renovate.yaml**: Automated dependency updates (daily 2 AM UTC)

Run locally: `./scripts/validate.sh`

## Troubleshooting

```bash
# Flux
flux check
flux logs --all-namespaces

# SOPS
kubectl get secret sops-age -n flux-system

# Apps
kubectl describe pod POD -n NAMESPACE
kubectl logs POD -n NAMESPACE

# Talos
talosctl health --nodes NODE_IP
talosctl logs -n NODE_IP
```

## Security Notes

**Critical**: The `age.agekey` in this repo is exposed and must be rotated immediately.

Remove sensitive files: `age.agekey`, `gerrit_key*`, `*.log`, `audit.txt`, `bak_*`

Apply security policies:

- Pod Security Standards (restricted mode)
- LimitRanges for resource defaults
- ResourceQuotas for namespace limits
- NetworkPolicies (default-deny)

## Resources

- [Flux Docs](https://fluxcd.io/docs/)
- [Talos Docs](https://www.talos.dev/docs/)
