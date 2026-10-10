# b.36h: one run of Hold36h.tla (tickets/Bugs/b.36h/tla), for the job scheduler only.
# The run script is tlcjob.sh (spec/tla/jobs/lib/tlcjob.sh, unchanged); it reads /work/runs and
# its LAST line is the one-line verdict "JOB-VERDICT <cfg>=<result>/<distinct>".
FROM eclipse-temurin:21-jre
WORKDIR /work
COPY . /work/
ENV XMX=12g DIRECT=2g BUDGET=3400 DISKCAP_MB=20000
RUN chmod 0755 /work/tlcjob.sh && java -version
CMD ["/work/tlcjob.sh"]
