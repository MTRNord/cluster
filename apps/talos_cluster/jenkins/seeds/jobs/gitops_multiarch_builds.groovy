// Seed job: GitOps multi-arch build pipeline
// Auto-creates the image build pipeline job from this definition

pipelineJob('gitops-multiarch-builds') {
  description('Multi-arch container image builds for GitOps infrastructure')

  triggers {
    githubPush()
    // Check for updates daily at 3 AM UTC
    cron('H 3 * * *')
  }

  parameters {
    choiceParam(name: 'BUILD_IMAGE', choices: ['matrix-backup', 'continuwuity', 'blog', 'bookwyrm', 'all'], description: 'Which image(s) to build')
  }

  definition {
    cps {
      script("""\
        pipeline {
          agent any

          options {
            timestamps()
            timeout(time: 4, unit: 'HOURS')
            buildDiscarder(logRotator(numToKeepStr: '10', artifactNumToKeepStr: '5'))
          }

          triggers {
            githubPush()
            // Nightly check for upstream updates (3 AM UTC)
            cron('H 3 * * *')
          }

          environment {
            REGISTRY = 'registry.midnightthoughts.space'
            REGISTRY_URL = 'https://registry.midnightthoughts.space'
            DOCKER_CONFIG = '/var/run/secrets/docker.io'
            COSIGN_KEY_PATH = '/var/run/secrets/cosign/key'
          }

          stages {
            stage('Prepare Build Environment') {
              steps {
                script {
                  echo "=== Preparing multi-arch build environment ==="
                  sh '''
                    docker run --rm --privileged multiarch/qemu-user-static --reset -p yes || true

                    ls -la /proc/sys/fs/binfmt_misc/ || echo "binfmt_misc not available"

                    docker buildx create --use --name multiarch-builder || docker buildx use multiarch-builder
                    docker buildx inspect --bootstrap
                  '''
                }
              }
            }

            stage('Clone Repository') {
              steps {
                checkout scm
              }
            }

            stage('Build Images') {
              parallel {
                stage('matrix-backup') {
                  when {
                    expression { params.BUILD_IMAGE == 'matrix-backup' || params.BUILD_IMAGE == 'all' }
                  }
                  steps {
                    script {
                      build_matrix_backup()
                    }
                  }
                }

                stage('continuwuity') {
                  when {
                    expression { params.BUILD_IMAGE == 'continuwuity' || params.BUILD_IMAGE == 'all' }
                  }
                  steps {
                    script {
                      build_continuwuity()
                    }
                  }
                }

                stage('blog') {
                  when {
                    expression { params.BUILD_IMAGE == 'blog' || params.BUILD_IMAGE == 'all' }
                  }
                  steps {
                    script {
                      build_blog()
                    }
                  }
                }

                stage('bookwyrm') {
                  when {
                    expression { params.BUILD_IMAGE == 'bookwyrm' || params.BUILD_IMAGE == 'all' }
                  }
                  steps {
                    script {
                      build_bookwyrm()
                    }
                  }
                }
              }
            }
          }

          post {
            always {
              cleanWs()
            }
            success {
              echo "✓ Build pipeline completed successfully!"
            }
            failure {
              echo "✗ Build pipeline failed!"
            }
          }
        }
      """.stripIndent())
      sandbox(true)
    }
  }
}

def build_matrix_backup() {
  echo "=== Building matrix-backup ==="
  docker.image('docker:latest').inside('--privileged') {
    sh '''
      set -eu

      mkdir -p ~/.docker
      cp ${DOCKER_CONFIG}/config.json ~/.docker/config.json 2>/dev/null || true

      TAG_TS=$(date -u +%Y%m%d-%H%M%S)
      TAG_SHA="sha-$(git rev-parse --short HEAD)"
      IMAGE="${REGISTRY}/mtrnord/cluster/matrix-backup"

      echo "Building multi-arch image: ${IMAGE}"
      echo "Tags: ${TAG_TS}, main, ${TAG_SHA}"

      docker buildx build \\
        --platform linux/amd64,linux/arm64 \\
        --tag "${IMAGE}:${TAG_TS}" \\
        --tag "${IMAGE}:main" \\
        --tag "${IMAGE}:${TAG_SHA}" \\
        --push \\
        -f apps/talos_cluster/image-builder/matrix-backup/Dockerfile \\
        apps/talos_cluster/image-builder/matrix-backup/

      docker run --rm \\
        -v ${COSIGN_KEY_PATH}:/cosign/key:ro \\
        -v ~/.docker:/root/.docker:ro \\
        gcr.io/projectsigstore/cosign:latest \\
        sign --key /cosign/key "${IMAGE}:${TAG_TS}"

      docker run --rm \\
        -v ${COSIGN_KEY_PATH}:/cosign/key:ro \\
        -v ~/.docker:/root/.docker:ro \\
        gcr.io/projectsigstore/cosign:latest \\
        sign --key /cosign/key "${IMAGE}:main"

      docker run --rm \\
        -v ${COSIGN_KEY_PATH}:/cosign/key:ro \\
        -v ~/.docker:/root/.docker:ro \\
        gcr.io/projectsigstore/cosign:latest \\
        sign --key /cosign/key "${IMAGE}:${TAG_SHA}"
    '''
  }
}

def build_continuwuity() {
  echo "=== Building continuwuity ==="
  docker.image('docker:latest').inside('--privileged') {
    sh '''
      set -eu

      mkdir -p ~/.docker
      cp ${DOCKER_CONFIG}/config.json ~/.docker/config.json 2>/dev/null || true

      TAG_SHA="sha-$(git rev-parse --short HEAD)"
      IMAGE="${REGISTRY}/mtrnord/cluster/continuwuity"

      echo "Building multi-arch image: ${IMAGE}"
      echo "Tags: main, ${TAG_SHA}"

      docker buildx build \\
        --platform linux/amd64,linux/arm64 \\
        --tag "${IMAGE}:main" \\
        --tag "${IMAGE}:${TAG_SHA}" \\
        --push \\
        -f apps/talos_cluster/image-builder/continuwuity/Dockerfile \\
        apps/talos_cluster/image-builder/continuwuity/

      docker run --rm \\
        -v ${COSIGN_KEY_PATH}:/cosign/key:ro \\
        -v ~/.docker:/root/.docker:ro \\
        gcr.io/projectsigstore/cosign:latest \\
        sign --key /cosign/key "${IMAGE}:main"

      docker run --rm \\
        -v ${COSIGN_KEY_PATH}:/cosign/key:ro \\
        -v ~/.docker:/root/.docker:ro \\
        gcr.io/projectsigstore/cosign:latest \\
        sign --key /cosign/key "${IMAGE}:${TAG_SHA}"
    '''
  }
}

def build_blog() {
  echo "=== Building blog ==="
  docker.image('docker:latest').inside('--privileged') {
    sh '''
      set -eu

      mkdir -p ~/.docker
      cp ${DOCKER_CONFIG}/config.json ~/.docker/config.json 2>/dev/null || true

      TAG_TS=$(date -u +%Y%m%d-%H%M%S)
      TAG_SHA="sha-$(git rev-parse --short HEAD)"
      IMAGE="${REGISTRY}/mtrnord/blog"

      echo "Building multi-arch image: ${IMAGE}"
      echo "Tags: ${TAG_TS}, latest, ${TAG_SHA}"

      docker buildx build \\
        --platform linux/amd64,linux/arm64 \\
        --tag "${IMAGE}:${TAG_TS}" \\
        --tag "${IMAGE}:latest" \\
        --tag "${IMAGE}:${TAG_SHA}" \\
        --push \\
        -f apps/talos_cluster/image-builder/blog/Dockerfile \\
        apps/talos_cluster/image-builder/blog/

      docker run --rm \\
        -v ${COSIGN_KEY_PATH}:/cosign/key:ro \\
        -v ~/.docker:/root/.docker:ro \\
        gcr.io/projectsigstore/cosign:latest \\
        sign --key /cosign/key "${IMAGE}:${TAG_TS}"

      docker run --rm \\
        -v ${COSIGN_KEY_PATH}:/cosign/key:ro \\
        -v ~/.docker:/root/.docker:ro \\
        gcr.io/projectsigstore/cosign:latest \\
        sign --key /cosign/key "${IMAGE}:latest"

      docker run --rm \\
        -v ${COSIGN_KEY_PATH}:/cosign/key:ro \\
        -v ~/.docker:/root/.docker:ro \\
        gcr.io/projectsigstore/cosign:latest \\
        sign --key /cosign/key "${IMAGE}:${TAG_SHA}"
    '''
  }
}

def build_bookwyrm() {
  echo "=== Building bookwyrm ==="
  docker.image('docker:latest').inside('--privileged') {
    sh '''
      set -eu

      mkdir -p ~/.docker
      cp ${DOCKER_CONFIG}/config.json ~/.docker/config.json 2>/dev/null || true

      VERSION=$(docker run --rm curlimages/curl:latest \\
        curl -sf "https://api.github.com/repos/bookwyrm-social/bookwyrm/releases/latest" | \\
        grep '"tag_name"' | head -1 | cut -d'"' -f4)

      if [ -z "$VERSION" ]; then
        echo "ERROR: could not determine upstream version"
        exit 1
      fi

      IMAGE="${REGISTRY}/mtrnord/cluster/bookwyrm"

      echo "Building multi-arch image: ${IMAGE}"
      echo "Tags: ${VERSION}, latest"

      docker buildx build \\
        --platform linux/amd64,linux/arm64 \\
        --build-arg VERSION="${VERSION}" \\
        --tag "${IMAGE}:${VERSION}" \\
        --tag "${IMAGE}:latest" \\
        --push \\
        -f apps/talos_cluster/image-builder/bookwyrm/Dockerfile \\
        apps/talos_cluster/image-builder/bookwyrm/

      docker run --rm \\
        -v ${COSIGN_KEY_PATH}:/cosign/key:ro \\
        -v ~/.docker:/root/.docker:ro \\
        gcr.io/projectsigstore/cosign:latest \\
        sign --key /cosign/key "${IMAGE}:${VERSION}"

      docker run --rm \\
        -v ${COSIGN_KEY_PATH}:/cosign/key:ro \\
        -v ~/.docker:/root/.docker:ro \\
        gcr.io/projectsigstore/cosign:latest \\
        sign --key /cosign/key "${IMAGE}:latest"
    '''
  }
}
