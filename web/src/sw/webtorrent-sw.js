/*
 * WebTorrent Service Worker — fork of webtorrent@3.0.21 lib/worker.js + lib/worker-server.js
 * (MIT, https://github.com/webtorrent/webtorrent). The message protocol with the page-side
 * BrowserServer (webtorrent lib/server.js) is unchanged.
 *
 * Fixes for streams that hang forever (video stuck at 0:00 or buffering although the data is
 * local):
 *
 * 1. Upstream posts every request to ALL window clients and takes the first answer. Another
 *    tab of the site answers too: a tab without that torrent replies 404 at once and wins; a
 *    background tab (throttled) or a tab that is later closed wins and the stream dies with
 *    its port. → Ask only the tab that issued the request (event.clientId). Fallback when
 *    the client is unknown: broadcast, but ignore 404 answers unless every tab says 404.
 *
 * 2. Upstream works around Firefox (no cancel() on SW ReadableStreams) with a 5 s timeout per
 *    pull: when the page hasn't produced the next chunk within 5 s (piece not downloaded yet),
 *    it cancels the page-side stream and resolves pull() WITHOUT enqueuing or closing. The
 *    stream then pulls again on a dead port and never completes. The flag that disables the
 *    workaround (`cancellable`) is module state set by a probe the page sends once, so it is
 *    lost whenever the browser restarts the idle worker (Chrome does after ~30 s) — after
 *    that every slow chunk hangs the response forever.
 *    → Never time out where cancel() works (Chromium, Safari). For Firefox keep an inactivity
 *    timeout, but error the stream so the media element sees a failure (and our player's
 *    watchdog reloads) instead of hanging.
 */

const FIREFOX = /firefox/i.test(self.navigator.userAgent)
const FIREFOX_PULL_TIMEOUT = 60_000

self.addEventListener("install", () => self.skipWaiting())
self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()))

self.addEventListener("fetch", (event) => {
  const { url } = event.request
  const base = self.registration.scope + "webtorrent/"
  if (!url.startsWith(base)) return
  if (url.startsWith(base + "keepalive/")) return event.respondWith(new Response())
  if (url.startsWith(base + "cancel/")) {
    // the page probes cancel() support with this request; kept for protocol compatibility
    return event.respondWith(new Response(new ReadableStream({ cancel() {} })))
  }
  event.respondWith(serve(event))
})

function ask(client, msg) {
  return new Promise((resolve) => {
    const { port1, port2 } = new MessageChannel()
    port1.onmessage = ({ data }) => resolve([data, port1])
    client.postMessage(msg, [port2])
  })
}

async function pickAnswer(event, msg) {
  const id = event.clientId || event.resultingClientId
  const own = id ? await self.clients.get(id) : null
  if (own) return ask(own, msg)
  const all = await self.clients.matchAll({ type: "window", includeUncontrolled: true })
  if (!all.length) return [{ status: 503, headers: {}, body: "no client" }, null]
  // first non-404 answer wins; 404 only if every tab says so
  return new Promise((resolve) => {
    let pending = all.length
    let notFound = null
    let done = false
    const drop = (port) => port && port.postMessage(false) // losers: let the page free its stream
    for (const c of all) {
      ask(c, msg).then(([data, port]) => {
        pending--
        if (done) return drop(port)
        if (data && data.status === 404) {
          if (notFound) drop(port)
          else notFound = [data, port]
          if (pending === 0) {
            done = true
            resolve(notFound)
          }
          return
        }
        done = true
        drop(notFound && notFound[1])
        resolve([data, port])
      })
    }
  })
}

async function serve(event) {
  const { url, method, headers, destination } = event.request
  const msg = {
    url,
    method,
    headers: Object.fromEntries(headers.entries()),
    scope: self.registration.scope,
    destination,
    type: "webtorrent",
  }
  const [data, port] = await pickAnswer(event, msg)
  if (!port) return new Response(data.body, data)

  let timer = null
  let closed = false
  const cleanup = () => {
    if (closed) return
    closed = true
    clearTimeout(timer)
    port.onmessage = null
    port.postMessage(false) // tell the page to destroy its file stream
  }

  if (data.body !== "STREAM") {
    cleanup()
    return new Response(data.body, data)
  }

  return new Response(
    new ReadableStream({
      pull(controller) {
        return new Promise((resolve) => {
          port.onmessage = ({ data: chunk }) => {
            clearTimeout(timer)
            if (chunk) {
              controller.enqueue(chunk) // Uint8Array
            } else {
              cleanup()
              controller.close() // end of the requested range
            }
            resolve()
          }
          if (FIREFOX && destination !== "document") {
            clearTimeout(timer)
            timer = setTimeout(() => {
              cleanup()
              controller.error(new Error("webtorrent stream stalled"))
              resolve()
            }, FIREFOX_PULL_TIMEOUT)
          }
          port.postMessage(true) // pull the next chunk
        })
      },
      cancel() {
        cleanup()
      },
    }),
    data,
  )
}
