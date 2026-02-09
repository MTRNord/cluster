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
    bcmath calendar ctype posix xmlreader xmlwriter sockets ffi xml readline

# Copy PHP ini and extension INI files before enabling them (cache layer friendly)
COPY 20-apcu.ini /etc/php/8.4/mods-available/20-apcu.ini
COPY 10-opcache.ini /etc/php/8.4/mods-available/10-opcache.ini
COPY www.conf /etc/php/8.4/fpm/pool.d/www.conf
COPY php.ini /etc/php/8.4/fpm/php.ini

# Install WordPress into the webroot
RUN wget -q https://wordpress.org/latest.zip \
    && unzip -q latest.zip -d /var/www/html/ \
    && rm latest.zip

# Install twenty-twenty-five theme
RUN wget -q https://downloads.wordpress.org/theme/twentytwentyfive.1.4.zip \
    && unzip -q -o twentytwentyfive.1.4.zip -d /var/www/html/wordpress/wp-content/themes/ \
    && rm twentytwentyfive.1.4.zip

# Install WP Super Cache plugin
RUN wget -q https://downloads.wordpress.org/plugin/wp-super-cache.3.0.3.zip \
    && unzip -q wp-super-cache.3.0.3.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm wp-super-cache.3.0.3.zip
# Install Redis Object Cache plugin
RUN wget -q https://downloads.wordpress.org/plugin/redis-cache.2.7.0.zip \
    && unzip -q redis-cache.2.7.0.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm redis-cache.2.7.0.zip
# Install ActivityPub plugin
RUN wget -q https://downloads.wordpress.org/plugin/activitypub.7.9.0.zip \
    && unzip -q activitypub.7.9.0.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm activitypub.7.9.0.zip
# Install Embed Optimizer
RUN wget -q https://downloads.wordpress.org/plugin/embed-optimizer.1.0.0-beta3.zip \
    && unzip -q embed-optimizer.1.0.0-beta3.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm embed-optimizer.1.0.0-beta3.zip
# Install Enhanced Responsive Images
RUN wget -q https://downloads.wordpress.org/plugin/auto-sizes.1.7.0.zip \
    && unzip -q auto-sizes.1.7.0.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm auto-sizes.1.7.0.zip
# Install Friends
RUN wget -q https://downloads.wordpress.org/plugin/friends.3.6.0.zip \
    && unzip -q friends.3.6.0.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm friends.3.6.0.zip
# Install Image Placeholders
RUN wget -q https://downloads.wordpress.org/plugin/dominant-color-images.1.2.0.zip \
    && unzip -q dominant-color-images.1.2.0.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm dominant-color-images.1.2.0.zip
# Install Image Prioritizer
RUN wget -q https://downloads.wordpress.org/plugin/image-prioritizer.1.0.0-beta3.zip \
    && unzip -q image-prioritizer.1.0.0-beta3.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm image-prioritizer.1.0.0-beta3.zip
# Install Index WP MySQL For Speed
RUN wget -q https://downloads.wordpress.org/plugin/index-wp-mysql-for-speed.1.5.6.zip \
    && unzip -q index-wp-mysql-for-speed.1.5.6.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm index-wp-mysql-for-speed.1.5.6.zip
# Install Modern Image Formats
RUN wget -q https://downloads.wordpress.org/plugin/webp-uploads.2.6.1.zip \
    && unzip -q webp-uploads.2.6.1.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm webp-uploads.2.6.1.zip
# Install Optimization Detective
RUN wget -q https://downloads.wordpress.org/plugin/optimization-detective.1.0.0-beta4.zip \
    && unzip -q optimization-detective.1.0.0-beta4.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm optimization-detective.1.0.0-beta4.zip
# Install Performance Lab
RUN wget -q https://downloads.wordpress.org/plugin/performance-lab.4.0.1.zip \
    && unzip -q performance-lab.4.0.1.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm performance-lab.4.0.1.zip
# Install Speculative Loading
RUN wget -q https://downloads.wordpress.org/plugin/speculation-rules.1.6.0.zip \
    && unzip -q speculation-rules.1.6.0.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm speculation-rules.1.6.0.zip
# Install WP Mail SMTP
#RUN wget -q https://downloads.wordpress.org/plugin/wp-mail-smtp.4.6.0.zip \
#    && unzip -q wp-mail-smtp.4.6.0.zip -d /var/www/html/wordpress/wp-content/plugins/ \
#    && rm wp-mail-smtp.4.6.0.zip

# Install Simply Gallery Block
#RUN wget -q https://downloads.wordpress.org/plugin/simply-gallery-block.3.2.8.zip \
#    && unzip -q simply-gallery-block.3.2.8.zip -d /var/www/html/wordpress/wp-content/plugins/ \
#    && rm simply-gallery-block.3.2.8.zip

# Install Plausible Analytics
RUN wget -q https://downloads.wordpress.org/plugin/plausible-analytics.latest-stable.zip \
    && unzip -q plausible-analytics.latest-stable.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm plausible-analytics.latest-stable.zip

# Install Public Post Preview
RUN wget -q https://downloads.wordpress.org/plugin/public-post-preview.3.0.1.zip \
    && unzip -q public-post-preview.3.0.1.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm public-post-preview.3.0.1.zip

# Install Opengraph plugin
RUN wget -q https://downloads.wordpress.org/plugin/opengraph.2.0.2.zip \
    && unzip -q opengraph.2.0.2.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm opengraph.2.0.2.zip

# Install surveyjs plugin
RUN wget -q https://downloads.wordpress.org/plugin/surveyjs.zip \
    && unzip -q surveyjs.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm surveyjs.zip

# Install Stop User Enumeration plugin
#RUN wget -q https://downloads.wordpress.org/plugin/stop-user-enumeration.1.7.5.zip \
#    && unzip -q stop-user-enumeration.1.7.5.zip -d /var/www/html/wordpress/wp-content/plugins/ \
#    && rm stop-user-enumeration.1.7.5.zip

# Install Antispam Bee plugin
RUN wget -q https://downloads.wordpress.org/plugin/antispam-bee.2.11.8.zip \
    && unzip -q antispam-bee.2.11.8.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm antispam-bee.2.11.8.zip

# Install Gallery Block Lightbox plugin
RUN wget -q https://downloads.wordpress.org/plugin/gallery-block-lightbox.zip \
    && unzip -q gallery-block-lightbox.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm gallery-block-lightbox.zip

# Install Complianz
#RUN wget -q https://downloads.wordpress.org/plugin/complianz-gdpr.7.4.3.zip \
#    && unzip -q complianz-gdpr.7.4.3.zip -d /var/www/html/wordpress/wp-content/plugins/ \
#    && rm complianz-gdpr.7.4.3.zip

# Install Google XML Sitemaps
RUN wget -q https://downloads.wordpress.org/plugin/google-sitemap-generator.4.1.23.zip \
    && unzip -q google-sitemap-generator.4.1.23.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm google-sitemap-generator.4.1.23.zip

# Install Akismet
RUN wget -q https://downloads.wordpress.org/plugin/akismet.5.6.zip \
    && unzip -q -o akismet.5.6.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm akismet.5.6.zip

# Install Podlove Publisher
RUN wget -q https://downloads.wordpress.org/plugin/podlove-podcasting-plugin-for-wordpress.4.3.3.zip \
    && unzip -q -o podlove-podcasting-plugin-for-wordpress.4.3.3.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm podlove-podcasting-plugin-for-wordpress.4.3.3.zip

# Install Podlove Web Player
RUN wget -q https://downloads.wordpress.org/plugin/podlove-web-player.5.9.2.zip \
    && unzip -q -o podlove-web-player.5.9.2.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm podlove-web-player.5.9.2.zip

# Install Podlove Subscribe button
RUN wget -q https://downloads.wordpress.org/plugin/podlove-subscribe-button.1.3.12.zip \
    && unzip -q -o podlove-subscribe-button.1.3.12.zip -d /var/www/html/wordpress/wp-content/plugins/ \
    && rm podlove-subscribe-button.1.3.12.zip

# Configure nginx: replace default site and enable wordpress config
RUN rm -f /etc/nginx/sites-enabled/default
COPY wordpress.conf /etc/nginx/sites-available/wordpress.conf
COPY nginx.conf /etc/nginx/nginx.conf
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
COPY opcache-preload.php /var/www/html/wordpress/opcache-preload.php

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
