# Use Ubuntu as the base image
FROM ubuntu:25.10

# Set the Timezone to UTC
ENV TZ=UTC

# Install Nginx, and other necessary Packages
RUN apt-get update && apt-get install -y \
    software-properties-common \
    nginx \
    curl \
    wget \
    sudo \
    unzip \
    && rm -rf /var/lib/apt/lists/*

# Add PHP repository
# RUN sudo add-apt-repository ppa:ondrej/php

RUN apt-get update && apt-get install -y \
    php8.4 php8.4-fpm php8.4-mysql php8.4-cli php8.4-gd \
    php8.4-curl php8.4-dom php8.4-imagick php8.4-mbstring php8.4-zip php8.4-intl \
    php8.4-redis redis-server cron \
    && rm -rf /var/lib/apt/lists/*
RUN phpenmod redis
ADD php.ini /etc/php/8.4/nginx/
COPY 10-opcache.ini /etc/php/8.4/mods-available/opcache.ini
COPY www.conf /etc/php/8.4/fpm/pool.d/www.conf

# Install Wordpress
RUN wget https://wordpress.org/latest.zip \
    && unzip latest.zip -d /var/www/html/ \
    && rm latest.zip


# Add Nginx config file
RUN rm /etc/nginx/sites-enabled/default
ADD wordpress.conf /etc/nginx/sites-available/
RUN ln -s /etc/nginx/sites-available/wordpress.conf /etc/nginx/sites-enabled/

# Copy redis.conf for redis-server
COPY redis.conf /redis.conf

COPY wellknown/client.json /var/www/html/well-known/client
COPY wellknown/server.json /var/www/html/well-known/server
COPY wellknown/support.json /var/www/html/well-known/support.json

# Add WordPress config file
RUN rm /var/www/html/wordpress/wp-config-sample.php
ADD wp-config.php /var/www/html/wordpress/wp-config.php
ADD advanced-cache.php /var/www/html/wordpress/wp-content/advanced-cache.php
ADD wp-cache-config.php /var/www/html/wordpress/wp-content/wp-cache-config.php
ADD object-cache.php /var/www/html/wordpress/wp-content/object-cache.php
# Change the user and Permission of WordPress root directory
RUN chown -R www-data:www-data /var/www/html/wordpress \
    && chmod -R 755 /var/www/html/wordpress
RUN chown -R www-data:www-data /var/www/html/well-known \
    && chmod -R 755 /var/www/html/well-known

# Exposing container to port 8080
EXPOSE 8080

# Copy the startup script and give excuitable permission
COPY startup.sh /usr/local/bin/startup.sh
RUN chmod +x /usr/local/bin/startup.sh

# Add cronjob: every 10 minutes, run wp-cron.php via curl
RUN echo '*/10 * * * * curl https://mtrnord.blog/wp-cron.php > /dev/null 2>&1' > /etc/cron.d/wp-cron
RUN chmod 0644 /etc/cron.d/wp-cron && crontab /etc/cron.d/wp-cron

# Ensure cron runs in the background with the app (use printf for cross-arch compatibility)
RUN printf '#!/bin/sh\ncron && exec "$@"\n' > /usr/local/bin/with-cron.sh && chmod +x /usr/local/bin/with-cron.sh

# Command to run the startup script with cron
CMD ["/usr/local/bin/with-cron.sh", "/usr/local/bin/startup.sh"]
