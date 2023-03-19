# Cluster Configs for Midnightthoughts and Nordgedanken

These are the current running setups at Midnightthoughts and Nordegedanken.

This is based on <https://github.com/fluxcd/flux2-kustomize-helm-example/tree/d54e250182ead1f4a00e9fd78b05dc9e0186246d>

## Prerequisites

You will need a Kubernetes cluster version 1.21 or newer.
For a quick local test, you can use [Kubernetes kind](https://kind.sigs.k8s.io/docs/user/quick-start/).
Any other Kubernetes setup will work as well though.

Install the Flux CLI on MacOS or Linux using Homebrew:

```sh
brew install fluxcd/tap/flux
```

Or install the CLI by downloading precompiled binaries using a Bash script:

```sh
curl -s https://fluxcd.io/install.sh | sudo bash
```

## Repository structure

The Git repository contains the following top directories:

- **apps** dir contains Helm releases with a custom configuration per cluster
- **infrastructure** dir contains common infra tools such as ingress-nginx and cert-manager
- **clusters** dir contains the Flux configuration per cluster (Note that staging isn't deployed anywhere at this time)

```
├── apps
│   ├── base
│   ├── production 
│   └── staging
├── infrastructure
│   ├── configs
│   └── controllers
└── clusters
    ├── production
    └── staging
```

### Applications

The apps configuration is structured into:

- **apps/base/** dir contains namespaces and Helm release definitions
- **apps/production/** dir contains the production Helm release values
- **apps/staging/** dir contains the staging values

```
./apps/
├── base
│   └── podinfo
│       ├── kustomization.yaml
│       ├── namespace.yaml
│       ├── release.yaml
│       └── repository.yaml
├── production
│   ├── kustomization.yaml
│   └── podinfo-patch.yaml
└── staging
    ├── kustomization.yaml
    └── podinfo-patch.yaml
```

### Infrastructure

The infrastructure is structured into:

- **infrastructure/controllers/** dir contains namespaces and Helm release definitions for Kubernetes controllers
- **infrastructure/configs/** dir contains Kubernetes custom resources such as cert issuers and networks policies

```
./infrastructure/
├── configs
│   ├── cluster-issuers.yaml
│   ├── network-policies.yaml
│   └── kustomization.yaml
└── controllers
    ├── cert-manager.yaml
    ├── ingress-nginx.yaml
    ├── weave-gitops.yaml
    └── kustomization.yaml
```

In **clusters/production/infrastructure.yaml** we replace the Let's Encrypt server value to point to the production API:

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1beta2
kind: Kustomization
metadata:
  name: infra-configs
  namespace: flux-system
spec:
  # ...omitted for brevity
  dependsOn:
    - name: infra-controllers
  patches:
    - patch: |
        - op: replace
          path: /spec/acme/server
          value: https://acme-v02.api.letsencrypt.org/directory
      target:
        kind: ClusterIssuer
        name: letsencrypt
```

Note that with `dependsOn` we tell Flux to first install or upgrade the controllers and only then the configs.
This ensures that the Kubernetes CRDs are registered on the cluster, before Flux applies any custom resources.

<!-- TODO setup bootstrap docs -->

## Useful things

Watch for the Helm releases being installed:

```console
$ watch flux get helmreleases --all-namespaces

NAMESPACE     NAME          REVISION SUSPENDED READY MESSAGE 
flux-system   weave-gitops  4.0.12    False     True  Release reconciliation succeeded
```

Watch kustomizations getting deployed:

```console
$ flux get kustomizations -w

NAME            REVISION                SUSPENDED       READY   MESSAGE                              
flux-system     main@sha1:21ebd912      False           True    Applied revision: main@sha1:21ebd912
infra-controllers       main@sha1:21ebd912      False   True    Applied revision: main@sha1:21ebd912
```

## TODOs

- Migrate old deployments here
  - [ ] Gitea
  - [x] Woodpecker
  - [ ] Docker repo
  - [ ] Traefik
  - [x] Certmanager
  - [ ] External DNS
  - [ ] Synapse
  - [ ] Mjolnir
  - [ ] Keycloak
  - [x] Prometheus/grafana
  - [x] Cosign
  - [ ] ...
- Port validate script
- Setup CI for github and woodpecker
- Verify sops is working as expected and then publish repo
