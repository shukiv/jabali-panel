<?php

declare(strict_types=1);

namespace App\Services\Notifications;

/**
 * Maps a notification type slug (e.g. 'backup_failure', 'ssl_expiring') to
 * a severity (info | warning | critical). Heuristic-based — admins can
 * override per-type routing in a later phase via notification_type_channels
 * if/when per-type granularity is added, but for v1 we key routing by
 * severity only.
 */
class SeverityResolver
{
    /** @var array<int, string> */
    private const CRITICAL_KEYWORDS = [
        'critical', 'fatal', 'crash', 'breach', 'compromise', 'failure', 'failed',
        'error', 'emergency', 'outage', 'down', 'attack', 'intrusion',
    ];

    /** @var array<int, string> */
    private const WARNING_KEYWORDS = [
        'warning', 'warn', 'expiring', 'expired', 'deprecated', 'throttle',
        'retry', 'slow', 'degraded', 'suspicious', 'unusual', 'quota',
    ];

    public function resolve(string $type): string
    {
        $slug = strtolower($type);

        foreach (self::CRITICAL_KEYWORDS as $kw) {
            if (str_contains($slug, $kw)) {
                return 'critical';
            }
        }
        foreach (self::WARNING_KEYWORDS as $kw) {
            if (str_contains($slug, $kw)) {
                return 'warning';
            }
        }

        return 'info';
    }
}
