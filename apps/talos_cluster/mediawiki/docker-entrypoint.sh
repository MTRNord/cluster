#!/bin/bash
set -e

# Apache needs these at runtime; create them in case the image layer didn't persist them
mkdir -p /var/run/apache2 /var/lock/apache2

# Start PHP-FPM as a daemon
/usr/local/sbin/php-fpm --daemonize

# Source Apache environment variables then run Apache in foreground
. /etc/apache2/envvars
exec /usr/sbin/apache2 -DFOREGROUND
