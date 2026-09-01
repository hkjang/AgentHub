# Put prime-agent back on PATH for login shells.
#
# Debian's /etc/profile rewrites PATH from scratch, which drops what the image's
# ENV added — and the browser terminal is a login shell.
PATH="/home/agent/.npm-global/bin:${PATH}"
export PATH
