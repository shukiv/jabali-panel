<?php

declare(strict_types=1);

namespace App\Services\Notifications;

use App\Models\DnsSetting;
use App\Models\NotificationChannel;
use App\Models\NotificationTypeChannel;

/**
 * Bridges the legacy `admin_email_recipients` DnsSetting into the v2
 * channel system by mirroring it into a well-known `default_email`
 * NotificationChannel and ensuring that channel receives every severity
 * by default.
 *
 * Why this exists: issue #102 — 298 callers still pass through
 * AdminNotificationService::send() and the only config many operators
 * have today is a comma-separated recipient string. Without this sync,
 * flipping the dispatcher on as the default would silently drop every
 * notification on a pristine install.
 *
 * Idempotent: safe to call on every "Save Notification Settings" click
 * and on dispatcher warmup.
 */
final class LegacyEmailChannelSync
{
    public const DEFAULT_NAME = 'default_email';

    /**
     * Ensure a default_email channel exists that mirrors
     * admin_email_recipients, and that it receives every severity.
     * Returns the channel, or null when no recipients are configured
     * (caller should NOT create an empty default channel — that would
     * fail every dispatch).
     *
     * @param  array<int, string>|string|null  $recipients
     */
    public function sync(array|string|null $recipients = null): ?NotificationChannel
    {
        $list = $this->normalize($recipients ?? DnsSetting::get('admin_email_recipients', ''));

        // If there's nothing to send to, tear down the auto-managed default
        // rather than leaving a half-broken channel around.
        if ($list === []) {
            NotificationChannel::query()
                ->where('name', self::DEFAULT_NAME)
                ->where('type', 'email')
                ->delete();

            return null;
        }

        /** @var NotificationChannel $channel */
        $channel = NotificationChannel::query()->updateOrCreate(
            ['name' => self::DEFAULT_NAME, 'type' => 'email'],
            ['config' => ['recipients' => $list], 'enabled' => true],
        );

        foreach (NotificationTypeChannel::SEVERITIES as $severity) {
            NotificationTypeChannel::query()->firstOrCreate(
                ['severity' => $severity, 'channel_id' => $channel->id],
                ['priority' => 0],
            );
        }

        return $channel->fresh();
    }

    /**
     * @param  array<int, string>|string|null  $input
     * @return array<int, string>
     */
    private function normalize(array|string|null $input): array
    {
        if ($input === null) {
            return [];
        }

        $raw = is_array($input) ? $input : explode(',', $input);
        $out = [];
        foreach ($raw as $r) {
            $r = trim((string) $r);
            if ($r === '') {
                continue;
            }
            if (filter_var($r, FILTER_VALIDATE_EMAIL) === false) {
                continue;
            }
            $out[] = $r;
        }

        return array_values(array_unique($out));
    }
}
