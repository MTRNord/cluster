#!/bin/bash

export SOPS_AGE_KEY_FILE="$(pwd)/age.agekey"
sops --ignore-mac --age=age1esjyg2qfy49awv0ptkzvpk425adczjr38m37w2mmcahzc4p8n54sll2nzh --decrypt --encrypted-regex '^(data|stringData|PASSWD|password|pass|postgresPassword|postgresqlPassword|redminePassword|smtpPassword|registration_shared_secret|shared_secret|secret)$' --in-place "$1"
