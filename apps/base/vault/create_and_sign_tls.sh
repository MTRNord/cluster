#!/bin/bash
export SERVICE=vault-server-tls

export NAMESPACE=vault

export SECRET_NAME=vault-server-tls

export TMPDIR=/tmp

export CSR_NAME=vault-csr

openssl genrsa -out ${TMPDIR}/vault.key 2048

cat <<EOF >${TMPDIR}/csr.conf
[req]
req_extensions = v3_req
distinguished_name = req_distinguished_name
[req_distinguished_name]
[ v3_req ]
basicConstraints = CA:FALSE
keyUsage = nonRepudiation, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names
[alt_names]
DNS.1 = *.${SERVICE}
DNS.2 = *.${SERVICE}.${NAMESPACE}
DNS.3 = *.${SERVICE}.${NAMESPACE}.svc
DNS.4 = *.${SERVICE}.${NAMESPACE}.svc.cluster.local
IP.1 = 127.0.0.1
EOF

openssl req -new -key ${TMPDIR}/vault.key -subj "/CN=system:node:${SERVICE}.${NAMESPACE}.svc;/O=system:nodes" -out ${TMPDIR}/server.csr -config ${TMPDIR}/csr.conf

cat <<EOF >${TMPDIR}/csr.yaml
apiVersion: certificates.k8s.io/v1
kind: CertificateSigningRequest
metadata:
  name: ${CSR_NAME}
spec:
    signerName: kubernetes.io/kubelet-serving
    groups:
    - system:authenticated
    request: $(base64 ${TMPDIR}/server.csr | tr -d '\n')
    signerName: kubernetes.io/kubelet-serving
    usages:
    - digital signature
    - key encipherment
    - server auth
EOF

kubectl create -f ${TMPDIR}/csr.yaml

