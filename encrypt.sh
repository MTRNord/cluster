#!/bin/bash

sops --age=age1esjyg2qfy49awv0ptkzvpk425adczjr38m37w2mmcahzc4p8n54sll2nzh --encrypt --encrypted-regex '^(data|stringData|password)$' --in-place "$1"