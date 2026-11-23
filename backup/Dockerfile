# The backup service (docs/architecture/backup.md). Build context: the
# repository root (compose.yaml).
#
# Its base is the database's own image, so pg_dump, pg_restore and the restore
# test's throwaway server are the database's version, with pgvector. restic
# (BSD-2-Clause) is a pinned release checked against its SHA-256.

FROM golang:1.26-alpine AS core
WORKDIR /src
COPY core/go.mod core/go.sum ./
COPY core/third_party/smbsrv/go.mod core/third_party/smbsrv/go.sum ./third_party/smbsrv/
RUN go mod download
COPY core/ ./
COPY VERSION /VERSION
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(cat /VERSION)" -o /out/core ./cmd/core

FROM alpine:3.22 AS restic
ARG TARGETARCH
COPY backup/fetch-restic.sh /fetch-restic.sh
RUN /fetch-restic.sh /out linux "$TARGETARCH"

FROM pgvector/pgvector:0.8.1-pg17 AS backup
# openssh-client: a remote repository over SFTP (CLOUDOS_BACKUP_REMOTE=sftp:…).
RUN apt-get update \
	&& apt-get install --yes --no-install-recommends ca-certificates openssh-client \
	&& rm -rf /var/lib/apt/lists/*
# The core's uid, which owns the data root: hard links and restored files
# stay its own. initdb needs the user to have a name.
RUN useradd --uid 65532 --user-group --home-dir /home/cloudos --create-home --shell /usr/sbin/nologin cloudos \
	&& install -d -o 65532 -g 65532 -m 0700 /cache
COPY --from=restic /out/restic /usr/local/bin/restic
COPY --from=core /out/core /core
# restic's cache of the repository's index, in a volume of its own (compose.yaml).
ENV RESTIC_CACHE_DIR=/cache
USER 65532:65532
ENTRYPOINT ["/core"]
CMD ["backup", "daemon"]

# The backup package's tests, with these very tools (make test-backup). Never
# deployed.
FROM backup AS test
COPY --from=golang:1.26-bookworm /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH CGO_ENABLED=0 GOPATH=/tmp/go GOCACHE=/tmp/gocache GOTOOLCHAIN=local
WORKDIR /tmp/src
COPY --chown=65532:65532 core/go.mod core/go.sum ./
COPY --chown=65532:65532 core/third_party/smbsrv/go.mod core/third_party/smbsrv/go.sum ./third_party/smbsrv/
RUN go mod download
COPY --chown=65532:65532 core/ ./
ENTRYPOINT []
CMD ["go", "test", "-count=1", "./internal/backup/..."]
