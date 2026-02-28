# Disaster Recovery Runbook — cluster-2025

Last updated: 2026-02-28

---

## Overview

This cluster has three layers of backup:

| Layer | Tool | Scope | Frequency | Retention | Storage |
|---|---|---|---|---|---|
| Application + Volumes | Velero (Kopia) | All namespaces + hcloud-volumes | 4× daily (0,6,12,18 UTC) | 14 days | `mtrnord-talos-velero` S3 bucket |
| Longhorn Volumes | Longhorn backup (incremental, full every 14d) | All Longhorn volumes (group: default) | Daily 02:00 UTC | 14 backups | `mtrnord-longhorn-backup` S3 bucket |
| Longhorn Config | Longhorn system-backup | Longhorn settings + metadata | Daily 05:00 UTC | 7 backups | `mtrnord-longhorn-backup` S3 bucket |
| PostgreSQL WAL | CNPG barman-cloud | postgres-cluster | Continuous + scheduled | 30 days | `mtrnord-talos-pg-backup` S3 bucket |

> **Key constraint**: hcloud-volumes CSI does NOT support snapshots. Velero uses Kopia (file-system copy into S3) for those volumes, which requires applications to be quiesced or tolerant of slightly inconsistent snapshots.

---

## Scenario 1: Single Application Recovery

Use this when one app's data is corrupt or accidentally deleted.

```bash
# List available backups
velero backup get

# See what's in a backup
velero backup describe cluster-backup-0000-20260228000000 --details

# Restore a single namespace
velero restore create --from-backup cluster-backup-0600-20260228060000 \
  --include-namespaces=<namespace> \
  --wait

# Restore a single resource
velero restore create --from-backup cluster-backup-0600-20260228060000 \
  --include-namespaces=<namespace> \
  --include-resources=persistentvolumeclaims,persistentvolumes \
  --wait
```

To restore to a different namespace (e.g. testing a restore without overwriting live data):
```bash
velero restore create --from-backup cluster-backup-0600-20260228060000 \
  --include-namespaces=myapp \
  --namespace-mappings myapp:myapp-restored \
  --wait
```

---

## Scenario 2: PostgreSQL Point-in-Time Recovery

CNPG continuously archives WALs to S3. This allows recovery to any point within the 30-day retention window.

### 2a. Recover to latest (e.g. after accidental table drop)

```bash
# Stop services that use postgres (reduce connections)
# Then edit cnpg-cluster.yaml to add a recovery bootstrap:

bootstrap:
  recovery:
    source: pg-s3-backup
    recoveryTarget:
      targetTLI: latest   # or use targetTime for PITR

externalClusters:
  - name: pg-s3-backup
    plugin:
      name: barman-cloud.cloudnative-pg.io
      parameters:
        barmanObjectName: hetzner-base-backup
        serverName: pg-cluster-v2   # original WAL archive path
```

Also change `plugins.parameters.serverName` to something new (e.g. `pg-cluster-v2-restored`) to avoid "expected empty archive" error.

### 2b. Point-in-Time Recovery (PITR)

```yaml
bootstrap:
  recovery:
    source: pg-s3-backup
    recoveryTarget:
      targetTime: "2026-02-27T20:00:00Z"   # RFC 3339, adjust as needed
```

### 2c. Important lessons from past migrations

- **DO NOT use `bootstrap.recovery.backup.name`** with the barman-cloud plugin — causes "missing Azure credentials" error. Always use `externalClusters` + `source`.
- **Set a different `serverName` in `plugins`** from the recovery `serverName` — otherwise WAL archiver fails with "expected empty archive".
- **If timeline mismatch error**: add `targetTLI: latest` or use `targetTime` before the timeline switch.
- **Deleting the CNPG Cluster object ALSO deletes the PVCs** by default. Before deleting for migration, verify `deletionPolicy` or set it to `retain`.
- After recovery: remove the `bootstrap` and `externalClusters` sections from `cnpg-cluster.yaml` once the cluster is healthy.

### 2d. Full postgres recovery procedure

```bash
# 1. Scale down apps using postgres
flux suspend kustomization apps

# 2. Delete the existing (broken) cluster
kubectl -n postgres-cluster delete cluster pg-cluster-v2

# 3. Edit gitops/infrastructure_talos/configs/cnpg-cluster.yaml:
#    - Add bootstrap + externalClusters as above
#    - Set storage.storageClass: hcloud-volumes (or longhorn if migrated)
#    - Set plugins.parameters.serverName: pg-cluster-v2-restored

# 4. Commit + push and let Flux apply
git add infrastructure_talos/configs/cnpg-cluster.yaml
git commit -m "recovery: restore pg-cluster-v2 from S3 backup"
git push

# 5. Watch recovery
kubectl -n postgres-cluster get cluster pg-cluster-v2 -w
kubectl -n postgres-cluster logs -l cnpg.io/cluster=pg-cluster-v2 -f

# 6. Once Ready: resume apps
flux resume kustomization apps

# 7. Cleanup: remove bootstrap/externalClusters from cnpg-cluster.yaml and commit
```

---

## Scenario 3: Longhorn Volume Recovery

### Longhorn Recurring Jobs

Backup target: `s3://mtrnord-longhorn-backup` (Hetzner Object Storage HEL1)

**Current configuration:**

| Job name | Type | Schedule | Group | Retain | Concurrency | Notes |
|---|---|---|---|---|---|---|
| `volume-backup` | `backup` | `0 2 * * *` (02:00) | default | 14 | 2 | Volume data → S3. Primary recovery source. Incremental, full every 14 days. |
| `post-backup-cleanup` | `snapshot-delete` | `0 3 * * *` (03:00) | default | 2 | 2 | Enforces max 2 snapshots per volume after backup runs. Prevents copy/move failures. |
| `system-backup` | `system-backup` | `0 5 * * *` (05:00) | — | 7 | — | Longhorn config/metadata backup. `volume-backup-policy: if-not-present`. |
| `filesystem-trim` | `filesystem-trim` | `0 4 * * *` (04:00) | default | — | 2 | Reclaim space from deleted files. Runs between backup (02:00) and system-backup (05:00). |

**Global Longhorn settings:**
- Max snapshots per volume: **5** (hard ceiling, monitoring before raising — `snapshot-delete` retain=2 enforces the soft limit, leaving 3 slots for system snapshots during replica rebuilds)
- Backup target: `s3://mtrnord-longhorn-backup` (Hetzner Object Storage HEL1)

**Why no `snapshot` or `snapshot-cleanup` job:**
- No `snapshot` job: with a low global snapshot limit, an hourly retain=24 would immediately hit the ceiling. Velero (6h) + Longhorn backup (daily) provide sufficient recovery points without in-cluster snapshots.
- No `snapshot-cleanup`: redundant when `backup` job does pre-backup cleanup and `snapshot-delete` enforces the count hard limit.

### 3a. Restore a single Longhorn volume from backup

**Via Longhorn UI (easiest):**
1. Go to Longhorn UI → Backup
2. Find the volume backup
3. Click Restore → enter a name for the restored volume
4. Once restored, create a PVC pointing to the new volume or update the app's PVC

**Via kubectl:**
```bash
# List available backups
kubectl -n longhorn-system get backups.longhorn.io

# Restore by creating a Volume CR pointing to the backup
kubectl apply -f - <<EOF
apiVersion: longhorn.io/v1beta2
kind: Volume
metadata:
  name: restored-volume
  namespace: longhorn-system
spec:
  fromBackup: "s3://your-bucket?backup=backup-name&volume=volume-name"
  numberOfReplicas: 2
  size: "10Gi"
EOF

# Then create a PVC that binds to it (via Longhorn UI or static PV/PVC)
```

### 3b. Restore from Longhorn system-backup

System backups capture the entire Longhorn state (volumes + settings).

```bash
# List system backups
kubectl -n longhorn-system get systembackups.longhorn.io

# Restore a system backup (this restores Longhorn settings + volumes)
# Do this in Longhorn UI: Settings → System Backup → Restore
# OR via CR:
kubectl apply -f - <<EOF
apiVersion: longhorn.io/v1beta2
kind: SystemRestore
metadata:
  name: restore-from-system-backup
  namespace: longhorn-system
spec:
  systemBackup: <system-backup-name>
EOF

kubectl -n longhorn-system get systemrestores -w
```

> **Warning**: System restore overwrites current Longhorn settings. Only use for full Longhorn recovery, not single-volume restore.

### 3c. Restore Longhorn volume via Velero CSI snapshot

If Velero captured a CSI snapshot (via the `longhorn-velero-vsc` VolumeSnapshotClass):

```bash
velero restore create --from-backup cluster-backup-0600-20260228060000 \
  --include-namespaces=<namespace> \
  --wait
```

Velero recreates the VolumeSnapshot and Longhorn restores the volume from it automatically.

### 3d. Which method to use?

| Situation | Best method |
|---|---|
| Single volume, recent data loss | Longhorn UI restore (3a) |
| Need data from >4 days ago | Velero restore (3c) — 14-day retention |
| Total Longhorn state loss | Longhorn system-backup restore (3b) |
| App namespace fully deleted | Velero namespace restore (Scenario 1) |

---

## Scenario 4: Full Cluster Recovery (Total Loss)

Use this when the entire cluster is gone and you need to rebuild from scratch.

### Prerequisites
- Terraform state intact (in Hetzner Cloud or backed up)
- Access to S3 buckets: `mtrnord-talos-velero` and `mtrnord-talos-pg-backup`
- Age private key (stored separately — see below)
- All secrets in gitops repo are SOPS-encrypted — need age key to decrypt

### Step 1: Rebuild infrastructure

```bash
cd cluster2025-talos/cloud
terraform apply    # recreates cloud VMs, network, firewall, kubeconfig

# For Proxmox nodes: re-apply Talos machineconfig
cd cluster2025-talos/proxmox
terraform apply -var-file=proxmox.tfvars
```

### Step 2: Bootstrap Flux

```bash
# Flux bootstrap will re-deploy all controllers and apps from gitops repo
flux bootstrap github \
  --owner=MTRNord \
  --repository=gitops \
  --branch=main \
  --path=clusters/talos_cluster
```

Or using your existing bootstrap method.

### Step 3: Provide age key for SOPS decryption

```bash
# The age private key must exist as a secret in flux-system
kubectl -n flux-system create secret generic sops-age \
  --from-file=age.agekey=/path/to/age.agekey
```

Without this, Flux cannot decrypt any SOPS-encrypted secrets (postgres credentials, velero credentials, etc.).

### Step 4: Wait for core infrastructure

```bash
# Wait for Velero, Longhorn, cert-manager, CNPG to be Ready
flux get kustomization
flux get helmrelease -A
kubectl -n velero get pods
kubectl -n longhorn-system get pods
```

### Step 5: Restore from Velero

```bash
# Find the most recent backup
velero backup get

# Full cluster restore (all namespaces)
velero restore create full-restore \
  --from-backup cluster-backup-0000-20260228000000 \
  --exclude-namespaces=velero,longhorn-system,kube-system,flux-system,cert-manager \
  --wait

# Flux will already manage velero/longhorn/etc — exclude those to avoid conflicts
```

### Step 6: Restore PostgreSQL

After Velero restores the `postgres-cluster` namespace, the CNPG operator will see the Cluster CR but the PVCs may not exist. If so, follow Scenario 2d above to recover from S3 WAL archives.

If Velero successfully restored the PVCs (hcloud-volumes Kopia backup), CNPG should recover automatically once the operator is running.

### Step 7: Verify

```bash
# Check all pods running
kubectl get pods -A | grep -v Running | grep -v Completed

# Check postgres
kubectl -n postgres-cluster get cluster pg-cluster-v2

# Check apps
flux get ks apps
```

---

## Key Backup Locations

| What | Where | Path |
|---|---|---|
| Velero backups (all namespaces + volumes) | Hetzner Object Storage HEL1 | `mtrnord-talos-velero` bucket |
| Postgres WAL archives | Hetzner Object Storage HEL1 | `mtrnord-talos-pg-backup/pg-base-backup/pg-cluster-v2/` |
| Postgres scheduled base backups | same bucket | `mtrnord-talos-pg-backup/pg-base-backup/pg-cluster-v2/base/` |
| Longhorn volume backups | Hetzner Object Storage HEL1 | `mtrnord-longhorn-backup` bucket |
| Longhorn system backups | same bucket | `mtrnord-longhorn-backup` bucket |
| GitOps repo | GitHub | MTRNord/gitops |
| Terraform state | Hetzner Cloud S3 / local | cluster2025-talos/cloud/terraform.tfstate |
| Age private key | Local machine | `~/.config/sops/age/keys.txt` or `age.agekey` |

> **CRITICAL**: The age private key is the master key for all cluster secrets. Store it in a password manager (Bitwarden, etc.) in addition to the local file. Without it you cannot decrypt any secret in the cluster.

---

## Recovery Decision Tree

```
Something is broken
    │
    ├── Single app data loss / corruption
    │       └── Velero restore of that namespace (Scenario 1)
    │
    ├── PostgreSQL data loss / corruption
    │       ├── Minor (table drop, bad migration)
    │       │       └── CNPG PITR to before the event (Scenario 2)
    │       └── Major (cluster deleted, storage gone)
    │               └── Full CNPG recovery from S3 (Scenario 2d)
    │
    ├── Longhorn volume corrupted
    │       └── Restore from Longhorn backup or Velero CSI snapshot (Scenario 3)
    │
    └── Total cluster loss
            └── Rebuild Terraform → Bootstrap Flux → Velero restore → CNPG restore (Scenario 4)
```

---

## Known Gotchas

- **hcloud-volumes Kopia backup consistency**: Kopia copies files from live pods. For databases other than postgres (which uses CNPG's own WAL-based backup), backups may be inconsistent if the app is writing during backup. Consider pre-backup hooks or accepting slight inconsistency.
- **Velero Schedule CRDs**: Velero CRDs (incl. `Schedule`) are installed by the HelmRelease (infra-controllers). Schedules themselves live in infra-configs which runs after controllers — this is why they're in `infrastructure_talos/configs/velero-schedules.yaml` not in the velero controller directory.
- **node-agent PodSecurity**: Kopia's `node-agent` DaemonSet requires `hostPath` volumes. The `velero` namespace must have `pod-security.kubernetes.io/enforce: privileged`.
- **Longhorn backup disk space**: Longhorn snapshots are space-expensive during generation. Prefer Velero (Kopia) for volume backups where possible.
