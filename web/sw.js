const CACHE_NAME = 'gordpool-api-v1';
const API_PATH = '/api/DayAheadPrices';

// Compute the next refresh window: after daily publish (~12:30 UTC) or after midnight UTC+30m.
function nextExpiry(now = new Date()) {
  const y = now.getUTCFullYear();
  const m = now.getUTCMonth();
  const d = now.getUTCDate();

  // Daily publish buffer (12:30 UTC to cover CET/CEST release).
  let publish = Date.UTC(y, m, d, 12, 30, 0, 0);
  if (publish <= now.getTime()) {
    publish = Date.UTC(y, m, d + 1, 12, 30, 0, 0);
  }

  // Midnight buffer (00:30 UTC next day) to roll to new "today".
  const midnight = Date.UTC(y, m, d + 1, 0, 30, 0, 0);

  return Math.min(publish, midnight);
}

self.addEventListener('install', () => {
  self.skipWaiting();
});

self.addEventListener('activate', event => {
  event.waitUntil(self.clients.claim());
});

self.addEventListener('fetch', event => {
  const { request } = event;
  if (request.method !== 'GET' || !request.url.includes(API_PATH)) {
    return; // Only handle API GETs.
  }

  event.respondWith(handleApiRequest(request));
});

async function handleApiRequest(request) {
  const cache = await caches.open(CACHE_NAME);
  const cached = await cache.match(request);
  const now = Date.now();

  if (cached) {
    const exp = parseInt(cached.headers.get('X-Expires-At') || '0', 10);
    if (exp && now < exp) {
      return cached;
    }
  }

  try {
    const network = await fetch(request);
    const exp = String(nextExpiry(new Date()));

    // Cache successful responses; handle no-body statuses separately.
    if (network.ok) {
      const noBodyStatus = network.status === 204 || network.status === 205 || network.status === 304;
      const headers = new Headers(network.headers);
      headers.set('X-Expires-At', exp);

      if (noBodyStatus) {
        const cachedResponse = new Response(null, {
          status: network.status,
          statusText: network.statusText,
          headers,
        });
        cache.put(request, cachedResponse);
      } else {
        const clone = network.clone();
        const body = await clone.arrayBuffer();
        const cachedResponse = new Response(body, {
          status: network.status,
          statusText: network.statusText,
          headers,
        });
        cache.put(request, cachedResponse);
      }
    }
    return network;
  } catch (err) {
    if (cached) {
      return cached; // Fallback to stale cache on network error.
    }
    throw err;
  }
}
