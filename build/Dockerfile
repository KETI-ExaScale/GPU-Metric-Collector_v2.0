# FROM registry.access.redhat.com/ubi7/ubi-minimal:latest
FROM nvidia/cuda:11.0.3-base-ubuntu18.04

ENV OPERATOR=/usr/local/bin/keti-gpu-metric-collector \
    USER_UID=root \
    USER_NAME=discovery
COPY _output/bin/keti-gpu-metric-collector ${OPERATOR}

ENTRYPOINT ["/usr/local/bin/keti-gpu-metric-collector"]

USER ${USER_UID}