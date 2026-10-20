# Prepare main/ and branch/ from immutable snapshots, each with its own built
# bin/agent-director, and snapshot.json containing both revisions/tree hashes.
# Base is the already-built integration harness image; build preparation may
# use the network, scheduler execution uses --network off.
ARG HARNESS_IMAGE=agent-director-test
FROM ${HARNESS_IMAGE}
USER root
RUN apt-get update && apt-get install -y --no-install-recommends python3 \
 && mkdir -p /work /out /go/pkg/mod /sandbox-tmp /opt/permission \
    /home/tester/.cache/go-build /home/tester/.config/agent-director \
    /home/tester/.local/share /home/tester/.bun/install/cache \
 && chown -R 1000:1000 /work /out /go /sandbox-tmp /opt/permission /home/tester \
 && chmod -R u+rwX /work /out /go /sandbox-tmp /opt/permission /home/tester
COPY --chown=1000:1000 main /opt/permission/main
COPY --chown=1000:1000 branch /opt/permission/branch
COPY --chown=1000:1000 snapshot.json runner.py /opt/permission/
USER tester
ENV AGENT_DIRECTOR_TEST_HARNESS=1 TMPDIR=/sandbox-tmp
WORKDIR /work
ENTRYPOINT ["python3", "-u", "/opt/permission/runner.py"]
