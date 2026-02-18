FROM alpine:3.20
RUN apk add --no-cache openssl
COPY generate-certs.sh /usr/local/bin/generate-certs.sh
RUN chmod +x /usr/local/bin/generate-certs.sh
CMD ["/usr/local/bin/generate-certs.sh"]
