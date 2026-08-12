#!/bin/bash

cd code/

go mod tidy
go build -o ../ffs .

echo "./ffs -config config.json -seed-email you@example.com -seed-handle root"
