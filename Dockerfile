ARG ARCH="amd64"
ARG OS="linux"
FROM quay.io/prometheus/busybox-${OS}-${ARCH}:latest
LABEL maintainer="https://github.com/EIETS"

ARG ARCH="amd64"
ARG OS="linux"
COPY .build/${OS}-${ARCH}/bamboo_exporter /bin/bamboo_exporter

EXPOSE 9117
USER nobody
ENTRYPOINT [ "/bin/bamboo_exporter" ]