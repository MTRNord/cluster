#!/bin/bash

sops --age=age1esjyg2qfy49awv0ptkzvpk425adczjr38m37w2mmcahzc4p8n54sll2nzh \
     --encrypt \
     --encrypted-regex '^(admin_password|extraCommands|key|clickhouseDatabaseURL|databaseURL|client_id|client_secret|secret_key_base|otp_secret|private_key|public_key|primaryKey|deterministicKey|keyDerivationSalt|token|clientId|secretKey|installationId|installationKey|uriOverride|adminToken.value|password.value|sql_password|erlangCookie|AUTHENTICATION_PASSWORD|ROOM_API_SECRET_KEY|adminPassword|configPassword|adminUser|configUser|MAIL_PASSWORD|APP_KEY|api_key|api_secret|keys|livekit_key|livekit_secret|secret_key|admin_pass|admin_email|mariadbPassword|mariadbRootPassword|privateKey|data|stringData|PASSWD|password|pass|postgresPassword|postgresqlPassword|redminePassword|smtpPassword|registration_shared_secret|shared_secret|secret|admin_token|integrationKey|rootPassword)$' \
     --in-place "$1"
