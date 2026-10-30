#!/bin/sh
#MISE description="Prepare project build and coverage output directories"
set -eu
umask 077
mkdir -p var/bin var/coverage
