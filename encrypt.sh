#!/bin/bash

sops --age=age1esjyg2qfy49awv0ptkzvpk425adczjr38m37w2mmcahzc4p8n54sll2nzh --encrypt --encrypted-regex '^(api_key|api_secret|keys|livekit_key|livekit_secret|secret_key|adminPassword|admin_pass|admin_email|mariadbPassword|mariadbRootPassword|privateKey|data|stringData|PASSWD|password|pass|postgresPassword|postgresqlPassword|redminePassword|smtpPassword|registration_shared_secret|shared_secret|secret)$' --in-place "$1"
