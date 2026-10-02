# TLC job image. Built and run only on gmahoney-test by jobsched.
FROM eclipse-temurin:21-jre
WORKDIR /work
COPY . /work/
RUN chmod 0755 /work/tlcjob.sh && java -version
CMD ["/work/tlcjob.sh"]
