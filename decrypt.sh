#!/bin/bash

export SOPS_AGE_KEY_FILE="$(pwd)/age.agekey"
sops --ignore-mac --age=age1esjyg2qfy49awv0ptkzvpk425adczjr38m37w2mmcahzc4p8n54sll2nzh --decrypt --encrypted-regex '^(AUTHENTICATION_PASSWORD|ROOM_API_SECRET_KEY|adminPassword|configPassword|adminUser|configUser|APP_KEY|api_key|api_secret|keys|livekit_key|livekit_secret|secret_key|adminPassword|admin_pass|admin_email|mariadbPassword|mariadbRootPassword|privateKey|data|stringData|PASSWD|password|pass|postgresPassword|postgresqlPassword|redminePassword|smtpPassword|registration_shared_secret|shared_secret|secret)$' --in-place "$1"
