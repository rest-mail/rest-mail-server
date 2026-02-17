FROM alpine:3.20

RUN apk add --no-cache dnsmasq bind-tools

COPY dnsmasq.conf /etc/dnsmasq.conf

EXPOSE 53/tcp 53/udp

HEALTHCHECK --interval=10s --timeout=5s --retries=3 \
    CMD nslookup mail1.test 127.0.0.1 || exit 1

ENTRYPOINT ["dnsmasq", "--no-daemon", "--log-facility=-"]
