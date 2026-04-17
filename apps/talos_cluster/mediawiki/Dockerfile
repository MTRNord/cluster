ARG MEDIAWIKI_VERSION=1.45.3
FROM mediawiki:${MEDIAWIKI_VERSION}

RUN apt-get update && apt-get install -y --no-install-recommends \
    librsvg2-bin \
    python3-pygments \
    git \
    php8.3-fpm \
  && git clone --depth 1 --branch REL1_45 \
       https://github.com/wikimedia/mediawiki-extensions-CodeMirror.git \
       /var/www/html/extensions/CodeMirror \
  && rm -rf /var/www/html/extensions/CodeMirror/.git \
  && pecl install redis \
  && docker-php-ext-enable apcu redis \
  && a2enmod proxy_fcgi setenvif mpm_event \
  && a2dismod mpm_prefork \
  && a2enconf php8.3-fpm \
  && apt-get purge -y git && apt-get autoremove -y \
  && rm -rf /var/lib/apt/lists/* /tmp/pear

COPY php-custom.ini /usr/local/etc/php/conf.d/zz-mediawiki-custom.ini
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

CMD ["/usr/local/bin/docker-entrypoint.sh"]
