/* eslint-env serviceworker */
// The web app's service worker. The build (pwa/vite-plugin.ts) fills in
// VERSION and PRECACHE, so every build is a new worker: the browser installs
// it in the background, and the app decides when to switch (see
// src/pwa/pwa.web.ts), never mid-session.
//
// Only the app itself is cached: the built files of this version. API
// requests, invite pages and anything else go to the network untouched; the
// app's data is end-to-end encrypted and lives in its own storage.
//
// The page is cached as "/", never "/index.html": servers commonly redirect
// the latter to "/" (Go's file server does), and a browser refuses a
// redirected response for a page load, which broke the whole site. For the
// same reason nothing redirected is ever stored, and a page load always
// falls back to the network rather than fail.
const VERSION = self.__VERSION__;
const PRECACHE = self.__PRECACHE__;
const CACHE = `supermatrix-app-${VERSION}`;
const SHELL = '/';

/** A stored copy that doesn't remember being redirected. */
async function clean(res) {
  if (!res.redirected) return res;
  return new Response(await res.blob(), {
    status: res.status,
    statusText: res.statusText,
    headers: res.headers,
  });
}

self.addEventListener('install', event => {
  event.waitUntil(
    (async () => {
      const cache = await caches.open(CACHE);
      for (const url of PRECACHE) {
        const res = await fetch(new Request(url, {cache: 'reload'}));
        if (!res.ok) throw new Error(`precache ${url}: ${res.status}`);
        await cache.put(url, await clean(res));
      }
      // No app window open (or none that loaded): nothing to interrupt, so
      // take over now. This is also what rescues a browser stuck on a broken
      // older worker, whose page can't load to ask for the switch.
      const open = await self.clients.matchAll({
        type: 'window',
        includeUncontrolled: true,
      });
      if (!open.length) await self.skipWaiting();
    })(),
  );
});

self.addEventListener('activate', event => {
  event.waitUntil(
    (async () => {
      for (const key of await caches.keys()) {
        if (key.startsWith('supermatrix-app-') && key !== CACHE)
          await caches.delete(key);
      }
      await self.clients.claim();
    })(),
  );
});

self.addEventListener('message', event => {
  if (event.data === 'skip-waiting') self.skipWaiting();
});

// Paths that belong to the server, not the app, if both share an origin.
const PASS = /^\/(v1\/|i\/|health$|\.well-known\/)/;

async function fromCache(key) {
  try {
    const hit = await caches.match(key, {cacheName: CACHE});
    return hit && !hit.redirected ? hit : null;
  } catch {
    return null;
  }
}

self.addEventListener('fetch', event => {
  const req = event.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin || PASS.test(url.pathname)) return;
  // Opening the app: this version's shell, instantly and offline.
  const key = req.mode === 'navigate' ? SHELL : req;
  event.respondWith(fromCache(key).then(hit => hit || fetch(req)));
});
