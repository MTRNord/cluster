#!/bin/bash

export SOPS_AGE_KEY_FILE="$(pwd)/age.agekey"
sops --ignore-mac \
     --decrypt \
     --encrypted-regex '^(admin_password|extraCommands|key|clickhouseDatabaseURL|databaseURL|client_id|client_secret|secret_key_base|otp_secret|private_key|public_key|primaryKey|deterministicKey|keyDerivationSalt|token|clientId|secretKey|installationId|installationKey|uriOverride|adminToken.value|password.value|sql_password|erlangCookie|PASSWD|AUTHENTICATION_PASSWORD|ROOM_API_SECRET_KEY|adminPassword|configPassword|adminUser|configUser|APP_KEY|api_key|api_secret|keys|livekit_key|livekit_secret|secret_key|adminPassword|admin_pass|admin_email|mariadbPassword|mariadbRootPassword|privateKey|data|stringData|PASSWD|password|pass|postgresPassword|postgresqlPassword|redminePassword|smtpPassword|registration_shared_secret|shared_secret|secret)$' \
     --in-place "$1"
