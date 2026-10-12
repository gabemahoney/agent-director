# b.ssz: one run of Hold36hCx.tla (tickets/Ideas/b.ssz/tla/src), for the job scheduler only
# (submit with --timeout 1h --memory 16g). The run script is tlcjob.sh
# (spec/tla/jobs/lib/tlcjob.sh, unchanged); it reads /work/runs and its LAST line is the
# one-line verdict "JOB-VERDICT <cfg>=<result>/<distinct>". Expected: PASS (distinct = 35589)
FROM eclipse-temurin:21-jre
WORKDIR /work
COPY . /work/
ENV XMX=8g DIRECT=2g BUDGET=1800 DISKCAP_MB=10000
RUN chmod 0755 /work/tlcjob.sh && java -version
CMD ["/work/tlcjob.sh"]
