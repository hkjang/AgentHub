# Put the agent toolchain back on PATH for login shells.
#
# Debian's /etc/profile rewrites PATH from scratch, which drops everything the
# image's ENV added — and the Goose session is a login shell, as is every command
# the agent runs through it. Without this `python` and `pip` exist in the image
# but not in the session that needs them.
PATH="/home/agent/.venv/bin:/opt/agenthub/venv/bin:${PATH}"
export PATH
