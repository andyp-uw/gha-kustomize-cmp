#!/bin/sh -l
git config --global --add safe.directory /github/workspace
go run /main.go
retVal=$?
if [ $retVal -ne 0 ]; then
  exit $retVal
fi
