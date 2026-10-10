# b.3ie: one run of B3ieSettle.tla (the settle-time model; tickets/Bugs/b.3ie/README.md), for the
# job scheduler only. The model is small (local sanity run: 158,868 distinct states in 6 s at
# MaxTime 3, MaxWrites 3); the largest job (MaxTime 4, MaxWrites 4) is expected in the low tens of
# millions. The run script is tlcjob.sh (spec/tla/jobs/lib/tlcjob.sh, unchanged); it reads
# /work/runs and its LAST line is the one-line verdict "JOB-VERDICT <cfg>=<result>/<distinct>".
FROM eclipse-temurin:21-jre
WORKDIR /work
COPY . /work/
ENV XMX=24g DIRECT=4g BUDGET=20000 DISKCAP_MB=60000
RUN chmod 0755 /work/tlcjob.sh && java -version
CMD ["/work/tlcjob.sh"]
