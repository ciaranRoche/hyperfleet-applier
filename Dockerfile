FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
WORKDIR /app
COPY bin/applier /app/applier
USER 65532:65532
ENTRYPOINT ["/app/applier"]
