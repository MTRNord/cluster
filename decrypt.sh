#!/bin/bash

export SOPS_AGE_KEY_FILE="$(pwd)/age.agekey"
sops --ignore-mac \
     --decrypt \
     --in-place "$1"
