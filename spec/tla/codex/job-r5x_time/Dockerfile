# b.ssz: one run of B146R5Cx.tla (tickets/Ideas/b.ssz/tla/src), for the job scheduler only
# (submit with --timeout 1h --memory 28g). The run script is tlcjob.sh
# (spec/tla/jobs/lib/tlcjob.sh, unchanged); it reads /work/runs and its LAST line is the
# one-line verdict "JOB-VERDICT <cfg>=<result>/<distinct>". Expected: PASS
FROM eclipse-temurin:21-jre
WORKDIR /work
COPY . /work/
ENV XMX=22g DIRECT=3g BUDGET=3300 DISKCAP_MB=30000
RUN chmod 0755 /work/tlcjob.sh && java -version
CMD ["/work/tlcjob.sh"]
