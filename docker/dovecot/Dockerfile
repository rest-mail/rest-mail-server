FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    dovecot-core \
    dovecot-imapd \
    dovecot-pop3d \
    dovecot-lmtpd \
    dovecot-pgsql \
    supervisor \
    rsyslog \
    ca-certificates \
    openssl \
    gettext-base \
    && rm -rf /var/lib/apt/lists/*

# Create vmail user (must match Postfix's virtual_uid_maps/virtual_gid_maps)
RUN groupadd -g 5000 vmail && \
    useradd -g vmail -u 5000 vmail -d /var/mail/vhosts -s /usr/sbin/nologin && \
    mkdir -p /var/mail/vhosts && \
    chown -R vmail:vmail /var/mail/vhosts

RUN mkdir -p /certs

COPY conf/ /etc/dovecot/templates/
COPY supervisord.conf /etc/supervisor/conf.d/dovecot.conf
COPY entrypoint.sh /entrypoint.sh

RUN chmod +x /entrypoint.sh

EXPOSE 143 993 110 995 24

HEALTHCHECK --interval=15s --timeout=5s --retries=3 \
    CMD doveadm service status imap || exit 1

ENTRYPOINT ["/entrypoint.sh"]
CMD ["supervisord", "-n", "-c", "/etc/supervisor/supervisord.conf"]
