# The existing project snapshot tool supplies source.tar and source-identity.json.
# Build only on the scheduler; runtime has no network and never starts Claude.
FROM golang:1.27.3-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends jq tini && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --shell /bin/bash builder
COPY source.tar /opt/snapshot/source.tar
COPY source-identity.json /opt/snapshot/source-identity.json
RUN mkdir -p /work /out /go/pkg/mod /home/builder/.cache/go-build \
 && tar -xf /opt/snapshot/source.tar -C /work \
 && chown -R builder:builder /work /out /go /home/builder
USER builder
ENV HOME=/home/builder AGENT_DIRECTOR_TEST_SANDBOX=1 GOTOOLCHAIN=local GOMAXPROCS=2 GOFLAGS=-p=1
WORKDIR /work
RUN go mod download
CMD ["/usr/bin/tini", "--", "bash", "/work/test/claude-live/prepare.sh", "/work", "/opt/snapshot/source-identity.json", "/opt/snapshot/source.tar", "/out/kit", "linux/amd64"]
