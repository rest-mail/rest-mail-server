FROM mcr.microsoft.com/devcontainers/base:ubuntu-24.04

RUN apt-get update && apt-get install -y \
    build-essential \
    curl \
    git \
    zsh \
    postgresql-client \
    dnsutils \
    net-tools \
    && rm -rf /var/lib/apt/lists/*
