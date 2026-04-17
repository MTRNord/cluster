ARG MEDIAWIKI_VERSION=1.45.3
FROM mediawiki:${MEDIAWIKI_VERSION} AS mediawiki-src

# Add CodeMirror while git is available in the source image
RUN apt-get update && apt-get install -y --no-install-recommends git \
  && git clone --depth 1 --branch REL1_45 \
       https://github.com/wikimedia/mediawiki-extensions-CodeMirror.git \
       /var/www/html/extensions/CodeMirror \
  && rm -rf /var/www/html/extensions/CodeMirror/.git \
  && apt-get purge -y git && apt-get autoremove -y \
  && rm -rf /var/lib/apt/lists/*

FROM php:8.3-fpm-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends \
    apache2 \
    librsvg2-bin \
    python3-pygments \
    libicu-dev \
    libonig-dev \
    liblua5.1-dev \
  && docker-php-ext-install -j$(nproc) calendar intl mbstring mysqli opcache \
  && pecl install apcu redis \
  && docker-php-ext-enable apcu redis \
  && pecl install LuaSandbox \
  && docker-php-ext-enable luasandbox \
  && a2enmod rewrite proxy_fcgi setenvif mpm_event \
  && a2dismod mpm_prefork || true \
  && rm -rf /var/lib/apt/lists/* /tmp/pear

COPY --from=mediawiki-src /var/www/html /var/www/html
COPY --from=mediawiki-src /var/www/data /var/www/data

COPY apache-vhost.conf /etc/apache2/sites-enabled/000-default.conf
COPY apache-fpm.conf /etc/apache2/conf-enabled/php-fpm.conf
COPY fpm-pool.conf /usr/local/etc/php-fpm.d/zz-mediawiki.conf
COPY php-custom.ini /usr/local/etc/php/conf.d/zz-mediawiki-custom.ini
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

RUN chmod +x /usr/local/bin/docker-entrypoint.sh

EXPOSE 80
CMD ["/usr/local/bin/docker-entrypoint.sh"]
