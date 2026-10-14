# Scheduler only: user submits this context; never execute on the dev host.
# Expected FAIL(Invariant_B21RelaunchPendingUnreachable_is_violated); observed NOT_RUN.
FROM eclipse-temurin:21-jre
WORKDIR /work
COPY . /work/
ENV XMX=2500m DIRECT=256m BUDGET=750 DISKCAP_MB=1500
CMD ["/work/tlcjob.sh"]
