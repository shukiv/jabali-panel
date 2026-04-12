<?php

declare(strict_types=1);

namespace App\Notifications\Channels;

use App\Models\NotificationChannel;
use App\Notifications\NotificationPayload;

/**
 * Common shape every channel driver must implement. Drivers are resolved by
 * ChannelDriverFactory per row type; each driver owns its config schema,
 * config validation, send, and test send.
 *
 * Drivers MUST NOT throw for expected errors (network, auth, rate limit) —
 * wrap in try/catch and surface via the returned ChannelResult so one
 * channel's failure never blocks siblings.
 */
interface ChannelDriver
{
    /**
     * Canonical type slug, e.g. 'email' | 'telegram' | 'ntfy' | 'web_push'.
     */
    public static function type(): string;

    /**
     * Validate the config array; return a list of error strings. Empty
     * array = valid. Used by the admin UI before save + by dispatch().
     *
     * @param  array<string, mixed>  $config
     * @return array<int, string>
     */
    public function validateConfig(array $config): array;

    /**
     * Deliver the payload via this configured channel. MUST NOT throw.
     */
    public function send(NotificationChannel $channel, NotificationPayload $payload): ChannelResult;
}
