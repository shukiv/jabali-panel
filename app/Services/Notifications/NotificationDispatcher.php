<?php

declare(strict_types=1);

namespace App\Services\Notifications;

use App\Models\NotificationChannel;
use App\Models\NotificationLog;
use App\Models\NotificationTypeChannel;
use App\Notifications\Channels\ChannelDriverFactory;
use App\Notifications\Channels\ChannelResult;
use App\Notifications\NotificationPayload;
use Illuminate\Support\Facades\Log;
use Throwable;

/**
 * v2 notification dispatcher. Resolves severity from type, looks up the
 * routing table for enabled channels at that severity, and fans out to
 * each driver with per-channel error isolation. Writes one
 * notification_logs row per channel attempt (legacy logger writes one
 * row per event — v2 writes one per channel so the audit trail is fine
 * grained).
 *
 * Returns true if at least one channel succeeded. This matches
 * AdminNotificationService::send()'s legacy bool contract so swapping
 * call paths behind the feature flag is transparent to the 298 callers.
 */
class NotificationDispatcher
{
    public function __construct(
        private readonly ChannelDriverFactory $factory,
        private readonly SeverityResolver $severity,
    ) {}

    /**
     * @param  array<string, mixed>  $context
     */
    public function dispatch(string $type, string $subject, string $message, array $context = []): bool
    {
        $severity = $this->severity->resolve($type);

        $payload = new NotificationPayload(
            type: $type,
            severity: $severity,
            subject: $subject,
            message: $message,
            context: $context,
            hostname: gethostname() ?: 'unknown',
        );

        $channels = $this->channelsFor($severity);

        if ($channels->isEmpty()) {
            NotificationLog::log($type, $subject, $message, [], 'skipped', $context, 'No channels configured for severity: '.$severity);

            return false;
        }

        $anySucceeded = false;

        foreach ($channels as $channel) {
            $result = $this->sendOne($channel, $payload);
            $this->logAttempt($channel, $payload, $result);

            if ($result->success) {
                $anySucceeded = true;
            }
        }

        return $anySucceeded;
    }

    /**
     * @return \Illuminate\Support\Collection<int, NotificationChannel>
     */
    private function channelsFor(string $severity): \Illuminate\Support\Collection
    {
        return NotificationTypeChannel::query()
            ->where('severity', $severity)
            ->orderByDesc('priority')
            ->with('channel')
            ->get()
            ->pluck('channel')
            ->filter(fn (?NotificationChannel $c) => $c !== null && $c->enabled)
            ->values();
    }

    private function sendOne(NotificationChannel $channel, NotificationPayload $payload): ChannelResult
    {
        if (! $this->factory->supports($channel->type)) {
            return ChannelResult::skipped("Unknown channel type: {$channel->type}");
        }

        try {
            $driver = $this->factory->for($channel->type);

            return $driver->send($channel, $payload);
        } catch (Throwable $e) {
            Log::error('NotificationDispatcher driver threw', [
                'channel' => $channel->name,
                'type' => $channel->type,
                'error' => $e->getMessage(),
            ]);

            return ChannelResult::failed($e->getMessage());
        }
    }

    private function logAttempt(
        NotificationChannel $channel,
        NotificationPayload $payload,
        ChannelResult $result,
    ): void {
        try {
            NotificationLog::log(
                $payload->type,
                $payload->subject,
                $payload->message,
                $result->recipients,
                $result->status,
                $payload->context + ['channel' => $channel->name],
                $result->error,
                $channel->type,
            );
        } catch (Throwable $e) {
            Log::error('NotificationDispatcher log failed', ['error' => $e->getMessage()]);
        }
    }
}
