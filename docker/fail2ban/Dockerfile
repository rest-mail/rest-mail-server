FROM alpine:3.19

RUN apk add --no-cache fail2ban iptables ip6tables

COPY jail.local /etc/fail2ban/jail.local
COPY filter.d/ /etc/fail2ban/filter.d/

RUN mkdir -p /var/log/restmail

CMD ["fail2ban-server", "-f", "--logtarget", "STDOUT"]
