#!/usr/bin/env bash

# i18n-ignore directory names, not a message
DIRS="${OUTPUT_SUBDIRS:-screenshots reports data}"

for dir in $DIRS; do
  mkdir -p "$OUTPUT_DIR/$dir"
done
