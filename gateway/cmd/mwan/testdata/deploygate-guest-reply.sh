#!/usr/bin/env bash
set -e

case "$*" in
    */sbin/route*) cat '{{.RoutePath}}' ;;
    *curl*) cat '{{.CurlPath}}' ;;
esac
