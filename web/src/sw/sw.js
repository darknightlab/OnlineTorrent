/*
 * The site's single service worker (scope "/", served as /sw.min.js; built by vite-plugin-pwa,
 * injectManifest, classic script). Two jobs:
 *
 * 1. WebTorrent streaming (webtorrent-sw.js, imported first so its fetch listener runs before
 *    workbox's): /webtorrent/* is answered by the tab that asked. Nothing below ever touches
 *    /webtorrent/* — it is not precached and is excluded from the navigation fallback.
 * 2. App shell: precache of the built assets (cleanupOutdatedCaches) and index.html for SPA
 *    navigations (not /api/, /meta/, /webseed/, /webtorrent/). No API caching: responses are
 *    permission-dependent and private.
 *
 * Updates ("prompt"): the very first install has no active worker → skipWaiting + clients.claim, so
 * the page is controlled at once and playback works without a reload (the engine waits for
 * "activated"). A later version waits until the user clicks 刷新 (SKIP_WAITING from
 * PWAUpdatePrompt); until then the old worker keeps serving, so a running stream isn't cut.
 */
import "./webtorrent-sw.js"
import { cleanupOutdatedCaches, createHandlerBoundToURL, precacheAndRoute } from "workbox-precaching"
import { NavigationRoute, registerRoute } from "workbox-routing"

self.addEventListener("install", () => {
  if (!self.registration.active) self.skipWaiting()
})
self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()))
self.addEventListener("message", (event) => {
  if (event.data && event.data.type === "SKIP_WAITING") self.skipWaiting()
})

cleanupOutdatedCaches()
precacheAndRoute(self.__WB_MANIFEST)
registerRoute(
  new NavigationRoute(createHandlerBoundToURL("/index.html"), {
    denylist: [/^\/api\//, /^\/meta\//, /^\/webseed\//, /^\/webtorrent\//],
  }),
)
