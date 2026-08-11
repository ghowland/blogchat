#!/bin/bash

go mod tidy
go build -o blog .

echo "./blog -config config.json -seed-email you@example.com -seed-handle root"
