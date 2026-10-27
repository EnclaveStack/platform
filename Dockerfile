# The EnclaveStack server image, ghcr.io/enclavestack/enclave (ADR-054). release.yml builds it from the release's
# own Linux programs, so it is not a build from source; build/release/linux_<arch>/enclave must exist first:
#
#   make release VERSION=vX.Y.Z && docker buildx build --platform linux/amd64,linux/arm64 -t enclave .
#
# Run it beside the Docker it drives (L5-004 §1a):
#   docker run -d -v /var/run/docker.sock:/var/run/docker.sock -v enclave-data:/data -p 7070:7070 ghcr.io/enclavestack/enclave
FROM alpine:3.24
ARG TARGETARCH
# The tools of networks across machines and VM links live in helper images the server builds (ADR-045, ADR-026).
RUN apk add --no-cache ca-certificates tzdata
COPY build/release/linux_${TARGETARCH}/enclave /usr/local/bin/enclave
VOLUME /data
EXPOSE 7070
ENTRYPOINT ["enclave"]
CMD ["server", "--listen", "0.0.0.0:7070", "--data-dir", "/data"]
