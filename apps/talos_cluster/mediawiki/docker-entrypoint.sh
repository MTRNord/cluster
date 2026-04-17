#!/bin/bash
set -e
php-fpm8.3 --daemonize
exec apache2-foreground
