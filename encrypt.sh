#!/bin/bash

if [ -z "$1" ]; then
    echo "No file specified. Encrypting default file: apps/talos_cluster/externaldns/release2.yaml"
    # Encrypt apps/talos_cluster/externaldns/release.yaml as a whole
    sops --age=age1esjyg2qfy49awv0ptkzvpk425adczjr38m37w2mmcahzc4p8n54sll2nzh \
        --encrypt \
        --encrypted-regex '^(extraArgs|harborAdminPassword|totpVaultKey|kimaiAppSecret|kimaiAdminPassword|GITHUB_CLIENT_ID|GITHUB_CLIENT_SECRET|GITHUB_PRIVATE_KEY|woosh|root_password|rspamd_password|pgdb_password|matrix_access_token|pgdb_remote_url|hmac_secret_key|adminPassword|adminEmail|jenkinsAdminEmail|securityRealm|gerrit.config|routing_key|DATABASE_URL|SMTP_PASSWORD|SECRET_KEY_BASE|admin_password|extraCommands|key|clickhouseDatabaseURL|databaseURL|client_id|client_secret|secret_key_base|otp_secret|private_key|public_key|primaryKey|deterministicKey|keyDerivationSalt|token|clientId|secretKey|installationId|installationKey|uriOverride|adminToken.value|password.value|sql_password|erlangCookie|AUTHENTICATION_PASSWORD|ROOM_API_SECRET_KEY|adminPassword|configPassword|adminUser|configUser|MAIL_PASSWORD|APP_KEY|api_key|api_secret|keys|livekit_key|livekit_secret|secret_key|admin_pass|admin_email|mariadbPassword|mariadbRootPassword|privateKey|data|stringData|PASSWD|password|pass|postgresPassword|postgresqlPassword|redminePassword|smtpPassword|registration_shared_secret|shared_secret|secret|admin_token|integrationKey|rootPassword|adminPassword|adminUser|adminEmail|emailPassword)$' \
        --in-place apps/talos_cluster/externaldns/release2.yaml
else
    echo "Encrypting file: $1"

    sops --age=age1esjyg2qfy49awv0ptkzvpk425adczjr38m37w2mmcahzc4p8n54sll2nzh \
        --encrypt \
        --encrypted-regex '^(harborAdminPassword|totpVaultKey|kimaiAppSecret|kimaiAdminPassword|GITHUB_CLIENT_ID|GITHUB_CLIENT_SECRET|GITHUB_PRIVATE_KEY|woosh|root_password|rspamd_password|pgdb_password|matrix_access_token|pgdb_remote_url|hmac_secret_key|adminPassword|adminEmail|jenkinsAdminEmail|securityRealm|gerrit.config|routing_key|DATABASE_URL|SMTP_PASSWORD|SECRET_KEY_BASE|admin_password|extraCommands|key|clickhouseDatabaseURL|databaseURL|client_id|client_secret|secret_key_base|otp_secret|private_key|public_key|primaryKey|deterministicKey|keyDerivationSalt|token|clientId|secretKey|installationId|installationKey|uriOverride|adminToken.value|password.value|sql_password|erlangCookie|AUTHENTICATION_PASSWORD|ROOM_API_SECRET_KEY|adminPassword|configPassword|adminUser|configUser|MAIL_PASSWORD|APP_KEY|api_key|api_secret|keys|livekit_key|livekit_secret|secret_key|admin_pass|admin_email|mariadbPassword|mariadbRootPassword|privateKey|data|stringData|PASSWD|password|pass|postgresPassword|postgresqlPassword|redminePassword|smtpPassword|registration_shared_secret|shared_secret|secret|admin_token|integrationKey|rootPassword|adminPassword|adminUser|adminEmail|emailPassword)$' \
        --in-place "$1"
fi
