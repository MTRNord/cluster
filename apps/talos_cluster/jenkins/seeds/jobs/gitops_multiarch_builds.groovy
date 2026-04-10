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
    choiceParam('BUILD_IMAGE', ['matrix-backup', 'continuwuity', 'blog', 'bookwyrm', 'all'], 'Which image(s) to build')
  }

  definition {
    cps {
      script("""\
        pipeline {
          agent {
            kubernetes {
              yaml '''
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
                    args: ['\${JENKINS_SECRET}', '\${JENKINS_NAME}']
                    env:
                    - name: JENKINS_TUNNEL
                      value: jenkins-operator-slave-jenkins.jenkins.svc.cluster.local:50000
                    - name: JENKINS_URL
                      value: http://jenkins-operator-http-jenkins.jenkins.svc.cluster.local:8080/
                  - name: docker
                    image: docker:dind
                    securityContext:
                      privileged: true
                    command:
                    - cat
                    tty: true
                    env:
                    - name: DOCKER_TLS_CERTDIR
                      value: ""
                    volumeMounts:
                    - name: docker-cache
                      mountPath: /var/lib/docker
                  volumes:
                  - name: docker-cache
                    emptyDir: {}
                  restartPolicy: Never
              '''
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
            REGISTRY_URL = 'https://registry.midnightthoughts.space'
            DOCKER_CONFIG = '/var/run/secrets/docker.io'
            COSIGN_KEY_PATH = '/var/run/secrets/cosign/key'
            PATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
          }

          stages {
            stage('Prepare Build Environment') {
              steps {
                sh '''
                  # Start dockerd in the background and wait for socket
                  dockerd > /tmp/dockerd.log 2>&1 &
                  DOCKER_PID=\$!
                  for i in \$(seq 1 30); do
                    if [ -S /var/run/docker.sock ]; then
                      break
                    fi
                    sleep 1
                  done
                  if [ ! -S /var/run/docker.sock ]; then
                    echo "ERROR: docker socket not available after 30 seconds"
                    kill \$DOCKER_PID 2>/dev/null || true
                    exit 1
                  fi
                  echo "Docker socket ready"
                  
                  # Setup multi-arch support
                  docker run --rm --privileged multiarch/qemu-user-static --reset -p yes || true
                  ls -la /proc/sys/fs/binfmt_misc/ || echo "binfmt_misc not available"
                  docker buildx create --use --name multiarch-builder || docker buildx use multiarch-builder
                  docker buildx inspect --bootstrap
                '''
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
              deleteDir()
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
  sh '''
    set -eu

    echo "=== Building matrix-backup ==="
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

def build_continuwuity() {
  sh '''
    set -eu

    echo "=== Building continuwuity ==="
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

def build_blog() {
  sh '''
    dockerd &
    sleep 10
    
    set -eu

    echo "=== Building blog ==="
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

def build_bookwyrm() {
  sh '''
    dockerd &
    sleep 10
    
    set -eu

    echo "=== Building bookwyrm ==="
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
