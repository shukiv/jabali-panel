<?php

declare(strict_types=1);

namespace App\Notifications\Channels;

use InvalidArgumentException;

/**
 * Resolves a ChannelDriver for a given channel type slug. Kept simple —
 * the map is small and compile-time, no container gymnastics needed.
 */
class ChannelDriverFactory
{
    /** @var array<string, class-string<ChannelDriver>> */
    private array $map = [
        'email' => EmailDriver::class,
        'telegram' => TelegramDriver::class,
        'ntfy' => NtfyDriver::class,
        'web_push' => WebPushDriver::class,
    ];

    public function for(string $type): ChannelDriver
    {
        if (! isset($this->map[$type])) {
            throw new InvalidArgumentException("Unknown channel type: {$type}");
        }

        /** @var ChannelDriver $driver */
        $driver = app($this->map[$type]);

        return $driver;
    }

    /**
     * Whether a driver exists for this type — lets the dispatcher skip
     * rows whose type was removed between versions.
     */
    public function supports(string $type): bool
    {
        return isset($this->map[$type]);
    }
}
