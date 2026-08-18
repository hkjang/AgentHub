# Put the agent toolchain back on PATH for login shells.
#
# Debian's /etc/profile rewrites PATH from scratch, which drops everything the
# image's ENV added. Login shells are how a custom runtime starts (`sh -lc`) and
# how most agents run shell commands, so without this `python`, `pip` and `conda`
# exist in the image but not in the session that needs them.
PATH="/opt/agenthub/venv/bin:/opt/conda/bin:${PATH}"
export PATH

# `conda activate` is a shell function, so it only exists once conda's own
# profile script has been sourced. Without it an agent can only use `conda run`.
if [ -f /opt/conda/etc/profile.d/conda.sh ]; then
  . /opt/conda/etc/profile.d/conda.sh
fi
