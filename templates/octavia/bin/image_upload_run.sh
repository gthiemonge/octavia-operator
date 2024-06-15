#!/bin/bash

set -xe

cp -f /var/lib/config-data/default/httpd.conf /etc/httpd/conf/httpd.conf

exec /usr/bin/run-httpd
