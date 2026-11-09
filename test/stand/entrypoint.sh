#!/bin/sh
set -e
mkdir -p /etc/ssh/authorized_keys
for u in bashuser tcshuser fishuser; do
  cp /stand/id_ed25519.pub "/etc/ssh/authorized_keys/$u"
  chmod 644 "/etc/ssh/authorized_keys/$u"
done
exec /usr/sbin/sshd -D -e
