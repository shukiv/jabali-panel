<?php

declare(strict_types=1);

namespace App\Notifications\Channels;

/**
 * Outcome of one channel delivery attempt. Fed to NotificationLog so
 * operators can see per-channel success/failure per event.
 */
final class ChannelResult
{
    private function __construct(
        public readonly bool $success,
        public readonly string $status,  // sent | failed | skipped
        public readonly ?string $error = null,
        /** @var array<int, string> */
        public readonly array $recipients = [],
    ) {}

    /**
     * @param  array<int, string>  $recipients
     */
    public static function sent(array $recipients = []): self
    {
        return new self(true, 'sent', null, $recipients);
    }

    public static function failed(string $error): self
    {
        return new self(false, 'failed', $error);
    }

    public static function skipped(string $reason): self
    {
        return new self(false, 'skipped', $reason);
    }
}
