# Use Ubuntu as the base image
FROM ubuntu:25.10

# Non-interactive frontend for apt and set timezone
ARG DEBIAN_FRONTEND=noninteractive
ENV TZ=UTC

# Install common packages, nginx, php and tools in a single layer for smaller image
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        gnupg \
        lsb-release \
        nginx \
        curl \
        wget \
        unzip \
        sudo \
        imagemagick \
        php8.4 \
        php8.4-fpm \
        #php8.4-mysql \
        php8.4-mysqlnd \
        php8.4-cli \
        php8.4-opcache \
        php8.4-exif \
        php8.4-gd \
        php8.4-curl \
        php8.4-dom \
        php8.4-imagick \
        php8.4-mbstring \
        php8.4-igbinary \
        php8.4-zip \
        php8.4-intl \
        php8.4-bz2 \
        php8.4-redis \
        php8.4-apcu \
        php8.4-bcmath \
        redis-server cron less nano \
    && rm -rf /var/lib/apt/lists/*

# Enable PHP extensions
RUN phpenmod -v 8.4 -s fpm \
    redis apcu exif gettext \
    iconv opcache bz2 igbinary curl dom \
    intl gd imagick mbstring zip mysqlnd curl \
    bcmath calendar ctype posix xmlreader xmlwriter

# Copy PHP ini and extension INI files before enabling them (cache layer friendly)
COPY 20-apcu.ini /etc/php/8.4/mods-available/20-apcu.ini
COPY 10-opcache.ini /etc/php/8.4/mods-available/10-opcache.ini
COPY www.conf /etc/php/8.4/fpm/pool.d/www.conf
COPY php.ini /etc/php/8.4/fpm/php.ini

# Install WordPress into the webroot
RUN wget -q https://wordpress.org/latest.zip \
    && unzip -q latest.zip -d /var/www/html/ \
    && rm latest.zip

# Configure nginx: replace default site and enable wordpress config
RUN rm -f /etc/nginx/sites-enabled/default
COPY wordpress.conf /etc/nginx/sites-available/wordpress.conf
RUN ln -s /etc/nginx/sites-available/wordpress.conf /etc/nginx/sites-enabled/wordpress.conf

# Copy redis config and well-known files
COPY redis.conf /redis.conf

COPY wellknown/client.json /var/www/html/well-known/client
COPY wellknown/server.json /var/www/html/well-known/server
COPY wellknown/support.json /var/www/html/well-known/support.json

# Add WordPress configuration and cache files
RUN rm -f /var/www/html/wordpress/wp-config-sample.php
COPY wp-config.php /var/www/html/wordpress/wp-config.php
COPY advanced-cache.php /var/www/html/wordpress/wp-content/advanced-cache.php
COPY wp-cache-config.php /var/www/html/wordpress/wp-content/wp-cache-config.php
COPY object-cache.php /var/www/html/wordpress/wp-content/object-cache.php

# Fix permissions for the webroot
RUN chown -R www-data:www-data /var/www/ \
    && chmod -R 755 /var/www/

# Install WP-CLI
RUN curl -fsSL -o /usr/local/bin/wp https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar \
    && chmod +x /usr/local/bin/wp

# Expose HTTP port
EXPOSE 8080

# Copy startup script and make it executable
COPY startup.sh /usr/local/bin/startup.sh
RUN chmod +x /usr/local/bin/startup.sh

# Add cronjob: every 10 minutes, run wp-cron.php via curl as www-data
# Note: cron files in /etc/cron.d must have the user field
RUN printf '*/10 * * * * www-data curl -fsS --retry 3 https://mtrnord.blog/wp-cron.php > /dev/null 2>&1\n' > /etc/cron.d/wp-cron \
    && chmod 0644 /etc/cron.d/wp-cron

# Ensure cron runs alongside the startup script
RUN printf '#!/bin/sh\ncron && exec "$@"\n' > /usr/local/bin/with-cron.sh && chmod +x /usr/local/bin/with-cron.sh

# Default command: run cron helper then startup
CMD ["/usr/local/bin/with-cron.sh", "/usr/local/bin/startup.sh"]
