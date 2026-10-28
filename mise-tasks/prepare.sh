#!/bin/sh
#MISE description="Prepare project build and coverage output directories"
set -eu
umask 077
mkdir -p var/bin var/coverage
if [ ! -f var/go.mod ]; then
    printf '%s\n' 'module github.com/echocat/sendspin-player-go/var' > var/go.mod
fi
