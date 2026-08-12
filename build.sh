#!/bin/bash

cd code/

go mod tidy
go build -o ../blogchat .

echo "./blogchat -config config.json -seed-email you@example.com -seed-handle root"
