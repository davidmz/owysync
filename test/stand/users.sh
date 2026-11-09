#!/bin/sh
# Creates the three stand users with "noisy" login shell rc files
# and the directories used by checks: proj (with the .owysync/remote marker) and nomarker (without).
set -e

add() { # name, shell
  if command -v useradd >/dev/null; then useradd -m -s "$2" "$1"; else adduser -D -s "$2" "$1"; fi
  # sshd refuses locked accounts; password login is disabled in sshd_config.
  echo "$1:$(head -c 24 /dev/urandom | base64)" | chpasswd
}

add bashuser /bin/bash
add tcshuser /bin/tcsh
add fishuser "$(command -v fish)"

install -o bashuser -g bashuser -m 644 /stand/rc/bashrc /home/bashuser/.bashrc
install -o tcshuser -g tcshuser -m 644 /stand/rc/cshrc /home/tcshuser/.cshrc
install -d -o fishuser -g fishuser /home/fishuser/.config /home/fishuser/.config/fish
install -o fishuser -g fishuser -m 644 /stand/rc/config.fish /home/fishuser/.config/fish/config.fish

for u in bashuser tcshuser fishuser; do
  install -d -o "$u" -g "$u" "/home/$u/proj" "/home/$u/proj/.owysync" "/home/$u/proj/.owysync/tmp" "/home/$u/nomarker"
  install -o "$u" -g "$u" -m 644 /dev/null "/home/$u/proj/.owysync/remote"
done

# Alpine's bash does not read .bashrc under sshd (Debian's bash is built to do so) -
# so load it through ~/.ssh/environment (PermitUserEnvironment in sshd_config).
if [ -f /etc/alpine-release ]; then
  install -d -o bashuser -g bashuser -m 700 /home/bashuser/.ssh
  echo 'BASH_ENV=/home/bashuser/.bashrc' > /home/bashuser/.ssh/environment
  chown bashuser:bashuser /home/bashuser/.ssh/environment
fi
