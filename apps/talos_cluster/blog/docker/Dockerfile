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

# Install PHP
RUN apt-get update && apt-get install -y \
    php8.4 php8.4-fpm php8.4-mysql php8.4-cli php8.4-gd \
    && rm -rf /var/lib/apt/lists/*
ADD php.ini /etc/php/8.4/nginx/

# Install Wordpress
RUN wget https://wordpress.org/latest.zip \
    && unzip latest.zip -d /var/www/html/ \
    && rm latest.zip

# Add Nginx config file
RUN rm /etc/nginx/sites-enabled/default
ADD wordpress.conf /etc/nginx/sites-available/
RUN ln -s /etc/nginx/sites-available/wordpress.conf /etc/nginx/sites-enabled/

COPY wellknown/client.json /var/www/html/well-known/client
COPY wellknown/server.json /var/www/html/well-known/server
COPY wellknown/support.json /var/www/html/well-known/support.json

# Add WordPress config file
RUN rm /var/www/html/wordpress/wp-config-sample.php
ADD wp-config.php /var/www/html/wordpress/wp-config.php

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

# Command to run the startup script
CMD ["/usr/local/bin/startup.sh"]
