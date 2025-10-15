#!/bin/bash

WP_CONFIG="/var/www/html/wordpress/wp-config.php"

sed -i "s/\${DB_NAME}/${DB_NAME}/g" $WP_CONFIG
sed -i "s/\${DB_USER}/${DB_USER}/g" $WP_CONFIG
sed -i "s/\${DB_PASSWORD}/${DB_PASSWORD}/g" $WP_CONFIG
sed -i "s/\${DB_HOST}/${DB_HOST}/g" $WP_CONFIG
sed -i "s/\${AUTH_KEY}/${AUTH_KEY}/g" $WP_CONFIG
sed -i "s/\${SECURE_AUTH_KEY}/${SECURE_AUTH_KEY}/g" $WP_CONFIG
sed -i "s/\${LOGGED_IN_KEY}/${LOGGED_IN_KEY}/g" $WP_CONFIG
sed -i "s/\${NONCE_KEY}/${NONCE_KEY}/g" $WP_CONFIG
sed -i "s/\${AUTH_SALT}/${AUTH_SALT}/g" $WP_CONFIG
sed -i "s/\${SECURE_AUTH_SALT}/${SECURE_AUTH_SALT}/g" $WP_CONFIG
sed -i "s/\${LOGGED_IN_SALT}/${LOGGED_IN_SALT}/g" $WP_CONFIG
sed -i "s/\${NONCE_SALT}/${NONCE_SALT}/g" $WP_CONFIG

redis-server /redis.conf &
service php8.4-fpm start
nginx -g 'daemon off;'
