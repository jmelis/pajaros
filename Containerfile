# birdquiz's UI ships inside the binary (server/main.go go:embed static), so
# the image needs nothing but the binary itself and somewhere writable for
# CACHE_DIR / USER_DB_PATH (a PVC, mounted by the deployment manifest).
#
# distroless/static rather than scratch: the server makes outbound HTTPS calls
# (Wikimedia, Google's OAuth token endpoint), and scratch carries no root CA
# bundle, so certificate verification would fail with "certificate signed by
# unknown authority" even though the binary itself is fine. distroless/static
# is scratch plus exactly that bundle, ~2 MB, no shell.
#
# Built for linux/amd64: the cross-compile happens on the host in
# `make server-linux` (CGO_ENABLED=0, so no toolchain needed), not here.
FROM gcr.io/distroless/static:latest

COPY server/birdquiz-server-linux-amd64 /birdquiz-server

# nobody. There is no /etc/passwd in distroless/static, so this has to be
# numeric; the deployment's fsGroup matches it so the mounted data volume is
# actually writable by this user.
USER 65534:65534

EXPOSE 8080
ENTRYPOINT ["/birdquiz-server"]
