#!/bin/bash

set -xe

TLS_SRC_DIR=/var/lib/config-data/tls
TLS_DST_DIR=/var/lib/config-data/merged

if [ -e $TLS_SRC_DIR/certs/internal.crt ]; then
    cp $TLS_SRC_DIR/certs/internal.crt $TLS_DST_DIR/
    chown default $TLS_DST_DIR/internal.crt
    chmod 400 $TLS_DST_DIR/internal.crt
fi

if [ -e $TLS_SRC_DIR/private/internal.key ]; then
    cp $TLS_SRC_DIR/private/internal.key $TLS_DST_DIR/
    chown default $TLS_DST_DIR/internal.key
    chmod 400 $TLS_DST_DIR/internal.key
fi
