FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    postfix \
    postfix-pgsql \
    supervisor \
    rsyslog \
    ca-certificates \
    openssl \
    libsasl2-modules \
    sasl2-bin \
    && rm -rf /var/lib/apt/lists/*

# Create vmail user for virtual mail delivery
RUN groupadd -g 5000 vmail && \
    useradd -g vmail -u 5000 vmail -d /var/mail/vhosts -s /usr/sbin/nologin && \
    mkdir -p /var/mail/vhosts && \
    chown -R vmail:vmail /var/mail/vhosts

# Create directories
RUN mkdir -p /etc/postfix/sql /var/spool/postfix/etc /certs

# Copy configuration templates
COPY conf/ /etc/postfix/templates/
COPY supervisord.conf /etc/supervisor/conf.d/postfix.conf
COPY rsyslog.conf /etc/rsyslog.d/postfix.conf
COPY entrypoint.sh /entrypoint.sh

RUN chmod +x /entrypoint.sh

EXPOSE 25 465 587

HEALTHCHECK --interval=15s --timeout=5s --retries=3 \
    CMD postfix status || exit 1

ENTRYPOINT ["/entrypoint.sh"]
CMD ["supervisord", "-n", "-c", "/etc/supervisor/supervisord.conf"]
