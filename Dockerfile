# ---------- build ----------
FROM golang:1.24-alpine AS build

WORKDIR /src

# A separate layer for the dependencies, so a source change does not repeat
# the download.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 makes a static binary. The SQLite driver is pure Go, so no
# C toolchain is necessary. The -s and -w flags remove the symbol table and
# the debug information.
RUN CGO_ENABLED=0 GOOS=linux go build \
	-trimpath -ldflags="-s -w" -o /blog .

# ---------- run ----------
# The static image holds the certificate bundle for the mail connection and
# the zone data for the time formats. It has no shell and no package
# manager, so the attack surface is the program only.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /blog /blog

# The volume mounts here. Every writable file of the program is in this
# directory, because the templates and the style sheet are inside the
# binary.
WORKDIR /data
VOLUME /data

ENV BLOG_DB_PATH=/data/blog.db
ENV BLOG_LISTEN=0.0.0.0:8080
EXPOSE 8080

USER nonroot:nonroot
ENTRYPOINT ["/blog"]

