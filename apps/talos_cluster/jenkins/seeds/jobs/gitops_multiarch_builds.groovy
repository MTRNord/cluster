// Seed job: GitOps multi-arch build pipeline
// Auto-creates the image build pipeline job from this definition

pipelineJob('gitops-multiarch-builds') {
  description('Multi-arch container image builds for GitOps infrastructure')

  triggers {
    githubPush()
    cron('H 3 * * *')
  }

  parameters {
    choiceParam('BUILD_IMAGE', ['matrix-backup', 'continuwuity', 'blog', 'bookwyrm', 'all'], 'Which image(s) to build')
  }

  definition {
    cps {
      script('''
        pipeline {
          agent {
            kubernetes {
              defaultContainer 'docker'
              yaml """
                apiVersion: v1
                kind: Pod
                metadata:
                  labels:
                    jenkins: agent
                    job: gitops-multiarch-builds
                spec:
                  serviceAccountName: jenkins-operator-jenkins
                  containers:
                  - name: jnlp
                    image: jenkins/inbound-agent:3248.v65ecb_254c298-6
                    env:
                    - name: JENKINS_TUNNEL
                      value: jenkins-operator-slave-jenkins.jenkins.svc.cluster.local:50000
                    - name: JENKINS_URL
                      value: http://jenkins-operator-http-jenkins.jenkins.svc.cluster.local:8080/
                    volumeMounts:
                    - name: workspace
                      mountPath: /home/jenkins/agent
                  - name: docker
                    image: docker:dind
                    securityContext:
                      privileged: true
                    env:
                    - name: DOCKER_TLS_CERTDIR
                      value: ""
                    volumeMounts:
                    - name: workspace
                      mountPath: /home/jenkins/agent
                    - name: registry-secret
                      mountPath: /var/run/secrets/docker.io/config.json
                      subPath: dockerconfig.json
                      readOnly: true
                    - name: cosign-secret
                      mountPath: /var/run/secrets/cosign/key
                      subPath: cosign.key
                      readOnly: true
                    - name: cosign-secret
                      mountPath: /var/run/secrets/cosign/password
                      subPath: cosign.password
                      readOnly: true
                  volumes:
                  - name: workspace
                    emptyDir: {}
                  - name: registry-secret
                    secret:
                      secretName: registry-credentials
                      defaultMode: 256
                  - name: cosign-secret
                    secret:
                      secretName: cosign-signing-key
                      defaultMode: 256
                  restartPolicy: Never
              """
            }
          }

          options {
            timestamps()
            timeout(time: 4, unit: 'HOURS')
            buildDiscarder(logRotator(numToKeepStr: '10', artifactNumToKeepStr: '5'))
          }

          triggers {
            githubPush()
            cron('H 3 * * *')
          }

          environment {
            REGISTRY = 'registry.midnightthoughts.space'
            // docker CLI reads credentials from $DOCKER_CONFIG/config.json
            DOCKER_CONFIG = '/var/run/secrets/docker.io'
          }

          stages {
            stage('Prepare Build Environment') {
              steps {
                sh """
                  apk add --no-cache git jq wget

                  # Allow git to operate on the workspace regardless of owner uid
                  git config --global --add safe.directory '*'

                  # Download cosign (same version as GitHub Actions)
                  wget -qO /usr/local/bin/cosign \\
                    https://github.com/sigstore/cosign/releases/download/v3.0.5/cosign-linux-amd64
                  chmod +x /usr/local/bin/cosign

                  # dockerd inside docker:dind starts asynchronously; wait for the socket
                  for i in \\$(seq 1 30); do
                    if [ -S /var/run/docker.sock ]; then
                      break
                    fi
                    echo "Waiting for docker daemon... (\\$i/30)"
                    sleep 2
                  done
                  if [ ! -S /var/run/docker.sock ]; then
                    echo "ERROR: docker daemon did not start within 60 seconds"
                    exit 1
                  fi

                  # Register QEMU binfmt handlers for cross-platform builds
                  # Pin to qemu-v8.1.5 and also copy the static binary into the DinD filesystem
                  # so BuildKit can find it at the registered path when injecting into arm64 containers
                  docker create --name binfmt-tmp --platform linux/amd64 tonistiigi/binfmt:qemu-v8.1.5
                  docker cp binfmt-tmp:/usr/bin/qemu-aarch64-static /usr/bin/qemu-aarch64-static || true
                  docker rm binfmt-tmp
                  docker run --rm --privileged --platform linux/amd64 \\
                    tonistiigi/binfmt:qemu-v8.1.5 --install all

                  # Create docker-container buildx builder (required for multi-platform --push)
                  # --oci-worker-no-process-sandbox: skip BuildKit's own QEMU injection and use
                  # the kernel binfmt F-flag fd directly, which avoids the wrong-qemu fallback
                  # that causes "Invalid ELF image for this architecture" in nested DinD
                  docker buildx create --name multiarch-builder --driver docker-container \\
                    --buildkitd-flags '--oci-worker-no-process-sandbox' \\
                    --platform linux/amd64,linux/arm64 2>/dev/null || true
                  docker buildx use multiarch-builder
                  docker buildx inspect --bootstrap
                """
              }
            }

            stage('Clone Repository') {
              steps {
                // checkout scm only works in Multibranch/Pipeline-from-SCM jobs;
                // this is an inline CPS pipeline so we clone explicitly.
                git url: 'https://github.com/MTRNord/cluster.git', branch: 'main'
              }
            }

            stage('Build Images') {
              parallel {
                stage('matrix-backup') {
                  when {
                    expression { params.BUILD_IMAGE == 'matrix-backup' || params.BUILD_IMAGE == 'all' }
                  }
                  steps {
                    script { build_matrix_backup() }
                  }
                }
                stage('continuwuity') {
                  when {
                    expression { params.BUILD_IMAGE == 'continuwuity' || params.BUILD_IMAGE == 'all' }
                  }
                  steps {
                    script { build_continuwuity() }
                  }
                }
                stage('blog') {
                  when {
                    expression { params.BUILD_IMAGE == 'blog' || params.BUILD_IMAGE == 'all' }
                  }
                  steps {
                    script { build_blog() }
                  }
                }
                stage('bookwyrm') {
                  when {
                    expression { params.BUILD_IMAGE == 'bookwyrm' || params.BUILD_IMAGE == 'all' }
                  }
                  steps {
                    script { build_bookwyrm() }
                  }
                }
              }
            }
          }

          post {
            success { echo "Build pipeline completed successfully!" }
            failure  { echo "Build pipeline failed!" }
          }
        }

        // ---------------------------------------------------------------------------
        // matrix-backup — Dockerfile: apps/talos_cluster/matrix-backup/backup-tool/
        // ---------------------------------------------------------------------------
        def build_matrix_backup() {
          sh \'\'\'
            set -eu
            echo "=== Building matrix-backup ==="

            TAG_TS="$(date -u +%Y%m%d-%H%M%S)"
            TAG_SHA="sha-$(git rev-parse --short HEAD)"
            IMAGE="registry.midnightthoughts.space/mtrnord/cluster/matrix-backup"

            docker buildx build \\
              --platform linux/amd64,linux/arm64 \\
              --tag "${IMAGE}:${TAG_TS}" \\
              --tag "${IMAGE}:main" \\
              --tag "${IMAGE}:${TAG_SHA}" \\
              --metadata-file /tmp/matrix-backup-meta.json \\
              --push \\
              -f apps/talos_cluster/matrix-backup/backup-tool/Dockerfile \\
              apps/talos_cluster/matrix-backup/backup-tool/

            DIGEST="$(jq -r \'."containerimage.digest"\' /tmp/matrix-backup-meta.json)"
            echo "Signing ${IMAGE}@${DIGEST}"
            { set +x; } 2>/dev/null
            COSIGN_PRIVATE_KEY="$(cat /var/run/secrets/cosign/key)"
            COSIGN_PASSWORD="$(cat /var/run/secrets/cosign/password)"
            COSIGN_EXPERIMENTAL=1 COSIGN_OCI_EXPERIMENTAL=1 \\
            COSIGN_PRIVATE_KEY="$COSIGN_PRIVATE_KEY" COSIGN_PASSWORD="$COSIGN_PASSWORD" \\
            cosign sign --yes --key env://COSIGN_PRIVATE_KEY \\
              --new-bundle-format=false \\
              --use-signing-config=false \\
              --registry-referrers-mode=oci-1-1 \\
              "${IMAGE}@${DIGEST}"
            { set -x; } 2>/dev/null
          \'\'\'
        }

        // ---------------------------------------------------------------------------
        // continuwuity — Dockerfile: apps/talos_cluster/continuwuity/
        // ---------------------------------------------------------------------------
        def build_continuwuity() {
          sh \'\'\'
            set -eu
            echo "=== Building continuwuity ==="

            TAG_TS="$(date -u +%Y%m%d-%H%M%S)"
            TAG_SHA="sha-$(git rev-parse --short HEAD)"
            IMAGE="registry.midnightthoughts.space/mtrnord/cluster/continuwuity"

            docker buildx build \\
              --platform linux/amd64,linux/arm64 \\
              --tag "${IMAGE}:main" \\
              --tag "${IMAGE}:${TAG_TS}" \\
              --tag "${IMAGE}:${TAG_SHA}" \\
              --metadata-file /tmp/continuwuity-meta.json \\
              --push \\
              -f apps/talos_cluster/continuwuity/Dockerfile \\
              apps/talos_cluster/continuwuity/

            DIGEST="$(jq -r \'."containerimage.digest"\' /tmp/continuwuity-meta.json)"
            echo "Signing ${IMAGE}@${DIGEST}"
            { set +x; } 2>/dev/null
            COSIGN_PRIVATE_KEY="$(cat /var/run/secrets/cosign/key)"
            COSIGN_PASSWORD="$(cat /var/run/secrets/cosign/password)"
            COSIGN_EXPERIMENTAL=1 COSIGN_OCI_EXPERIMENTAL=1 \\
            COSIGN_PRIVATE_KEY="$COSIGN_PRIVATE_KEY" COSIGN_PASSWORD="$COSIGN_PASSWORD" \\
            cosign sign --yes --key env://COSIGN_PRIVATE_KEY \\
              --new-bundle-format=false \\
              --use-signing-config=false \\
              --registry-referrers-mode=oci-1-1 \\
              "${IMAGE}@${DIGEST}"
            { set -x; } 2>/dev/null
          \'\'\'
        }

        // ---------------------------------------------------------------------------
        // blog — Dockerfile: apps/talos_cluster/blog/docker/
        // ---------------------------------------------------------------------------
        def build_blog() {
          sh \'\'\'
            set -eu
            echo "=== Building blog ==="

            TAG_TS="$(date -u +%Y%m%d-%H%M%S)"
            TAG_SHA="sha-$(git rev-parse --short HEAD)"
            IMAGE="registry.midnightthoughts.space/mtrnord/blog"

            docker buildx build \\
              --platform linux/amd64,linux/arm64 \\
              --tag "${IMAGE}:latest" \\
              --tag "${IMAGE}:${TAG_TS}" \\
              --tag "${IMAGE}:${TAG_SHA}" \\
              --metadata-file /tmp/blog-meta.json \\
              --push \\
              -f apps/talos_cluster/blog/docker/Dockerfile \\
              apps/talos_cluster/blog/docker/

            DIGEST="$(jq -r \'."containerimage.digest"\' /tmp/blog-meta.json)"
            echo "Signing ${IMAGE}@${DIGEST}"
            { set +x; } 2>/dev/null
            COSIGN_PRIVATE_KEY="$(cat /var/run/secrets/cosign/key)"
            COSIGN_PASSWORD="$(cat /var/run/secrets/cosign/password)"
            COSIGN_EXPERIMENTAL=1 COSIGN_OCI_EXPERIMENTAL=1 \\
            COSIGN_PRIVATE_KEY="$COSIGN_PRIVATE_KEY" COSIGN_PASSWORD="$COSIGN_PASSWORD" \\
            cosign sign --yes --key env://COSIGN_PRIVATE_KEY \\
              --new-bundle-format=false \\
              --use-signing-config=false \\
              --registry-referrers-mode=oci-1-1 \\
              "${IMAGE}@${DIGEST}"
            { set -x; } 2>/dev/null
          \'\'\'
        }

        // ---------------------------------------------------------------------------
        // bookwyrm — clones upstream source, applies our patch, skips if tag exists
        // Mirrors the logic in .github/workflows/build-bookwyrm.yaml
        // ---------------------------------------------------------------------------
        def build_bookwyrm() {
          sh \'\'\'
            set -eu
            echo "=== Building bookwyrm ==="

            # Determine latest upstream release
            VERSION="$(wget -qO - \'https://api.github.com/repos/bookwyrm-social/bookwyrm/releases/latest\' \\
              | jq -r \'.tag_name\')"
            if [ -z "$VERSION" ]; then
              echo "ERROR: could not determine upstream bookwyrm version"
              exit 1
            fi
            echo "Upstream version: ${VERSION}"

            IMAGE="registry.midnightthoughts.space/mtrnord/bookwyrm"

            # Skip if this tag already exists in the registry (same logic as GH Actions)
            TAGS_JSON="$(wget -qO - \'https://registry.midnightthoughts.space/v2/mtrnord/bookwyrm/tags/list\' 2>/dev/null || echo \'{}\')"
            if echo "$TAGS_JSON" | jq -e --arg v "$VERSION" \'(.tags // []) | contains([$v])\' > /dev/null 2>&1; then
              echo "Tag ${VERSION} already exists in registry — skipping build"
              exit 0
            fi
            echo "Tag ${VERSION} not found in registry — building"

            # Clone bookwyrm source at the release tag
            rm -rf /tmp/bookwyrm
            git clone --depth=1 --branch "${VERSION}" \\
              https://github.com/bookwyrm-social/bookwyrm.git /tmp/bookwyrm

            # Apply our Dockerfile patch (tolerates if it doesn\'t apply cleanly)
            PATCH="$(pwd)/apps/talos_cluster/bookwyrm/dockerfile.patch"
            cd /tmp/bookwyrm
            if git apply --check "$PATCH" 2>/dev/null; then
              git apply "$PATCH"
              echo "Dockerfile patch applied"
            else
              echo "Patch does not apply cleanly — proceeding without it"
            fi

            docker buildx build \\
              --platform linux/amd64,linux/arm64 \\
              --tag "${IMAGE}:${VERSION}" \\
              --tag "${IMAGE}:latest" \\
              --metadata-file /tmp/bookwyrm-meta.json \\
              --push \\
              .

            DIGEST="$(jq -r \'."containerimage.digest"\' /tmp/bookwyrm-meta.json)"
            echo "Signing ${IMAGE}@${DIGEST}"
            { set +x; } 2>/dev/null
            COSIGN_PRIVATE_KEY="$(cat /var/run/secrets/cosign/key)"
            COSIGN_PASSWORD="$(cat /var/run/secrets/cosign/password)"
            COSIGN_EXPERIMENTAL=1 COSIGN_OCI_EXPERIMENTAL=1 \\
            COSIGN_PRIVATE_KEY="$COSIGN_PRIVATE_KEY" COSIGN_PASSWORD="$COSIGN_PASSWORD" \\
            cosign sign --yes --key env://COSIGN_PRIVATE_KEY \\
              --new-bundle-format=false \\
              --use-signing-config=false \\
              --registry-referrers-mode=oci-1-1 \\
              "${IMAGE}@${DIGEST}"
            { set -x; } 2>/dev/null
          \'\'\'
        }
      '''.stripIndent())
      sandbox(true)
    }
  }
}
