<?php

declare(strict_types=1);

return [
    /*
    |--------------------------------------------------------------------------
    | Unified Notifications v2
    |--------------------------------------------------------------------------
    |
    | While this flag is `false` the v2 code path (channel-based routing)
    | is fully bypassed and AdminNotificationService::send() keeps the
    | legacy email-only behaviour. Flip to true once Phase 6 is deployed
    | and the default channel rows have been seeded.
    |
    */

    'v2_enabled' => (bool) env('NOTIFICATIONS_V2_ENABLED', true),

    /*
    |--------------------------------------------------------------------------
    | Web Push (VAPID)
    |--------------------------------------------------------------------------
    |
    | VAPID keypair — PUBLIC is shipped to the browser for
    | pushManager.subscribe(); PRIVATE stays in .env on the server and is
    | used to sign outgoing push payloads. Generated once during install.sh
    | (phase 5 follow-up).
    |
    */

    'web_push' => [
        'subject' => env('NOTIFICATIONS_VAPID_SUBJECT', 'mailto:admin@localhost'),
        'public_key' => env('NOTIFICATIONS_VAPID_PUBLIC_KEY'),
        'private_key' => env('NOTIFICATIONS_VAPID_PRIVATE_KEY'),
    ],

];
