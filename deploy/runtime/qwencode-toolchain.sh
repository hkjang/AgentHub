# Put the agent toolchain back on PATH for login shells.
#
# Debian's /etc/profile rewrites PATH from scratch, which drops everything the
# image's ENV added — and the Qwen Code session is a login shell, as is every
# command the agent runs through it. Without this `python` and `pip` exist in the
# image but not in the session that needs them.
# The agent's own environment leads, then the toolchain the image ships. The
# first one is on the home volume so `pip install` works even when the security
# profile mounts the root filesystem read-only, which is the default.
PATH="/home/agent/.venv/bin:/opt/agenthub/venv/bin:/home/agent/.npm-global/bin:${PATH}"
export PATH
