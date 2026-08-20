# Put the agent toolchain back on PATH for login shells.
#
# Debian's /etc/profile rewrites PATH from scratch, which drops everything the
# image's ENV added — and the BrowserCode session is a login shell, as is every
# command the agent runs through it.
PATH="/home/agent/.venv/bin:/opt/agenthub/venv/bin:${PATH}"
export PATH
