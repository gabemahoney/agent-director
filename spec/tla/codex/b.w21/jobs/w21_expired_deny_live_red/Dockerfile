# b.w21: scheduler only. Never build or run on the development host.
# Expected FAIL(Temporal_properties_were_violated); no observed result yet.
FROM eclipse-temurin:21-jre
WORKDIR /work
COPY . /work/
ENV XMX=2500m DIRECT=256m BUDGET=750 DISKCAP_MB=1500
CMD ["/work/tlcjob.sh"]
