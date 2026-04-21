ARG MEDIAWIKI_VERSION=1.45.3
FROM mediawiki:${MEDIAWIKI_VERSION} AS mediawiki-src

# Clone extensions not shipped in the MW tarball
RUN apt-get update && apt-get install -y --no-install-recommends git \
  && git clone --depth 1 --branch REL1_45 \
       https://github.com/wikimedia/mediawiki-extensions-CodeMirror.git \
       /var/www/html/extensions/CodeMirror \
  && rm -rf /var/www/html/extensions/CodeMirror/.git \
  && for EXT in \
       ArticleCreationWorkflow \
       Babel \
       BetaFeatures \
       ContactPage \
       CreditsSource \
       Disambiguator \
       GuidedTour \
       NearbyPages \
       MobileFrontend \
       ReadingLists \
       RelatedArticles \
       RevisionSlider \
       RSS \
       TorBlock \
        ParserFunctions \
        Scribunto \
        JsonConfig \
        Translate \
        UniversalLanguageSelector \
     ; do \
       git clone --depth 1 --branch REL1_45 \
         "https://gerrit.wikimedia.org/r/mediawiki/extensions/${EXT}" \
         "/var/www/html/extensions/${EXT}" \
       && rm -rf "/var/www/html/extensions/${EXT}/.git"; \
     done \
  && git clone --depth 1 --branch REL1_45 \
       https://gerrit.wikimedia.org/r/mediawiki/extensions/cldr \
       /var/www/html/extensions/CLDR \
  && rm -rf /var/www/html/extensions/CLDR/.git \
  && git clone --depth 1 \
       https://gerrit.wikimedia.org/r/mediawiki/extensions/GPGMail \
       /var/www/html/extensions/GPGMail \
  && rm -rf /var/www/html/extensions/GPGMail/.git \
  && apt-get purge -y git && apt-get autoremove -y \
  && rm -rf /var/lib/apt/lists/*

FROM php:8.3-fpm-bookworm

# Slow stable layer: compile PHP extensions (only changes if extensions/versions change)
RUN apt-get update && apt-get install -y --no-install-recommends \
    libicu-dev \
    libonig-dev \
    liblua5.1-dev \
  && docker-php-ext-install -j$(nproc) calendar intl mbstring mysqli opcache \
  && pecl install apcu redis LuaSandbox \
  && docker-php-ext-enable apcu redis luasandbox \
  && apt-get purge -y --auto-remove libicu-dev libonig-dev liblua5.1-dev \
  && rm -rf /var/lib/apt/lists/* /tmp/pear

# Runtime packages + Apache (add new deps here without busting the layer above)
RUN apt-get update && apt-get install -y --no-install-recommends \
    apache2 \
    ghostscript \
    gnupg \
    imagemagick \
    librsvg2-bin \
    poppler-utils \
    python3-pygments \
  && a2enmod rewrite proxy_fcgi setenvif mpm_event headers \
  && a2dismod mpm_prefork || true \
  && rm -rf /var/lib/apt/lists/*

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
