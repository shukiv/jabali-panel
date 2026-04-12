<?php

declare(strict_types=1);

namespace App\Notifications;

/**
 * Immutable request shape handed to channel drivers. Built inside
 * NotificationDispatcher from the legacy send() arguments so all drivers
 * receive the same normalised data.
 */
final class NotificationPayload
{
    public function __construct(
        public readonly string $type,
        public readonly string $severity,
        public readonly string $subject,
        public readonly string $message,
        /** @var array<string, mixed> */
        public readonly array $context = [],
        public readonly ?string $hostname = null,
    ) {}

    /**
     * Plain-text rendering used by email, ntfy, telegram — one shape so
     * drivers never re-do the formatting.
     */
    public function toPlainText(): string
    {
        $out = $this->message."\n\n---\n".
            "Server: {$this->hostname}\n".
            'Time: '.now()->format('Y-m-d H:i:s')."\n";

        if (! empty($this->context)) {
            $out .= "\nDetails:\n";
            foreach ($this->context as $key => $value) {
                $out .= "- {$key}: ".(is_scalar($value) ? (string) $value : json_encode($value))."\n";
            }
        }

        return $out;
    }
}
