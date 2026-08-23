# Put the Orca CLI back on PATH for login shells.
#
# Debian's /etc/profile rewrites PATH from scratch, which drops what the image's
# ENV added — and the browser terminal is a login shell. The CLI installs itself
# under the runtime user's home on first serve, which is why this points there.
PATH="/home/agent/.local/bin:${PATH}"
export PATH
