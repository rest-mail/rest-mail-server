# Base image pinned by digest for reproducible builds (OSI-17). This is an
# interactive developer workstation container, not a deployed service, so the
# runtime hardening applied to the mail-server services does not apply here.
FROM mcr.microsoft.com/devcontainers/base:ubuntu-24.04@sha256:d94c97dd9cacf183d0a6fd12a8e87b526e9e928307674ae9c94139139c0c6eae

RUN apt-get update && apt-get install -y \
    build-essential \
    curl \
    git \
    zsh \
    postgresql-client \
    dnsutils \
    net-tools \
    && rm -rf /var/lib/apt/lists/*
