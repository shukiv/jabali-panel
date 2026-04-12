// Jabali Panel — Web Push service worker.
// Registered from the admin panel once an admin opts in to browser push.
// Receives push events signed by the server-side VAPID private key and
// displays them as native OS notifications.

self.addEventListener('install', (event) => {
    self.skipWaiting();
});

self.addEventListener('activate', (event) => {
    event.waitUntil(self.clients.claim());
});

self.addEventListener('push', (event) => {
    // Payload is set by App\Notifications\Channels\WebPushDriver::send():
    //   { title, body, severity, type, ts }
    let data = {};
    try {
        data = event.data ? event.data.json() : {};
    } catch (_) {
        data = { title: 'Jabali', body: event.data ? event.data.text() : '' };
    }

    const title = data.title || 'Jabali';
    const options = {
        body: data.body || '',
        icon: '/favicon.ico',
        badge: '/favicon.ico',
        tag: data.type || 'jabali-notification',
        // Critical-severity events stay visible until the admin interacts.
        requireInteraction: data.severity === 'critical',
        data: {
            severity: data.severity,
            type: data.type,
            ts: data.ts,
        },
    };

    event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener('notificationclick', (event) => {
    event.notification.close();
    // Focus an open admin tab if one exists; otherwise open the panel root.
    event.waitUntil(
        clients.matchAll({ type: 'window', includeUncontrolled: true }).then((windowClients) => {
            for (const client of windowClients) {
                if (client.url.includes('/jabali-admin/') && 'focus' in client) {
                    return client.focus();
                }
            }
            return clients.openWindow ? clients.openWindow('/jabali-admin/') : null;
        })
    );
});
