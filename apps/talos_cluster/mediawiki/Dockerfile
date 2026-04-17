ARG MEDIAWIKI_VERSION=1.45.3
FROM mediawiki:${MEDIAWIKI_VERSION}

RUN apt-get update && apt-get install -y --no-install-recommends \
    librsvg2-bin \
    python3-pygments \
    git \
  && git clone --depth 1 --branch REL1_45 \
       https://github.com/wikimedia/mediawiki-extensions-CodeMirror.git \
       /var/www/html/extensions/CodeMirror \
  && rm -rf /var/www/html/extensions/CodeMirror/.git \
  && apt-get purge -y git && apt-get autoremove -y \
  && rm -rf /var/lib/apt/lists/*

COPY php-custom.ini /usr/local/etc/php/conf.d/mediawiki-custom.ini
