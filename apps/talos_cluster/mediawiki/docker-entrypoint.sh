#!/bin/bash
set -e

# Start PHP-FPM as a daemon
/usr/local/sbin/php-fpm --daemonize

# Source Apache environment variables then run Apache in foreground
. /etc/apache2/envvars
exec /usr/sbin/apache2 -DFOREGROUND
