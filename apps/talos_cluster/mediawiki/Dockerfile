ARG MEDIAWIKI_VERSION=1.45.3
FROM mediawiki:${MEDIAWIKI_VERSION}

RUN apt-get update && apt-get install -y --no-install-recommends \
    librsvg2-bin \
    python3-pygments \
  && rm -rf /var/lib/apt/lists/*

COPY php-custom.ini /usr/local/etc/php/conf.d/mediawiki-custom.ini
