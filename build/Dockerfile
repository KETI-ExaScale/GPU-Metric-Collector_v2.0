# FROM registry.access.redhat.com/ubi7/ubi-minimal:latest
# FROM nvidia/cuda:11.0.3-base-ubuntu18.04
FROM nvidia/cuda:11.4.3-base-ubuntu20.04

ENV OPERATOR=/usr/local/bin/keti-gpu-metric-collector \
    USER_UID=root \
    USER_NAME=discovery
COPY _output/bin/keti-gpu-metric-collector ${OPERATOR}

ENV NVIDIA_VISIBLE_DEVICES=all
ENV NVIDIA_DRIVER_CAPABILITIES=utility

ENTRYPOINT ["/usr/local/bin/keti-gpu-metric-collector"]

USER ${USER_UID}