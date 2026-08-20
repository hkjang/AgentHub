# The browser in this runtime

A Chromium is already running in this container with remote debugging on
loopback. Attach to it rather than launching your own — a second browser has no
window anybody can see and no profile that survives the Pod:

```js
const info = await (await fetch('http://127.0.0.1:9222/json/version')).json()
await session.connect({ wsUrl: info.webSocketDebuggerUrl })
```

`session.connect()` with no arguments will not find it. It looks for the
DevTools port file in the standard profile directories, and current Chromium
does not write that file when it is started with `--user-data-dir`.

The browser's own sandbox is disabled, because it cannot start inside this
container with it on. The container is the boundary instead: this Pod runs as an
unprivileged user with no cluster credentials and whatever network policy the
platform applied. Treat every page you open as untrusted, and do not paste
secrets into one.

The profile lives on the home volume, so cookies and logins you establish survive
a restart of this runtime.
