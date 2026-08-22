# Put the review engine back on PATH for login shells.
#
# Debian's /etc/profile rewrites PATH from scratch, which drops what the image's
# ENV added — and the browser terminal is a login shell. Without this `ocr`
# exists in the image but not in the session that wants it.
PATH="/home/agent/.npm-global/bin:${PATH}"
export PATH
