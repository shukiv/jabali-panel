<?php

declare(strict_types=1);

namespace App\Notifications\Channels;

use App\Models\NotificationChannel;
use App\Models\PushSubscription;
use App\Notifications\NotificationPayload;
use Illuminate\Support\Facades\Log;
use Minishlink\WebPush\Subscription as MinishlinkSubscription;
use Minishlink\WebPush\WebPush;
use Throwable;

/**
 * Web Push (VAPID) driver.
 *
 * Config on the NotificationChannel row is small:
 *   ['subject' => 'mailto:admin@example.com']
 * VAPID keypair is shared across all web_push channels and lives in env
 * (config/notifications.php: web_push.public_key / private_key). Per-admin
 * browser subscriptions live in the push_subscriptions table — each
 * send() fans out to every enabled subscription and prunes 410-Gone rows.
 */
final class WebPushDriver implements ChannelDriver
{
    public static function type(): string
    {
        return 'web_push';
    }

    public function validateConfig(array $config): array
    {
        $errors = [];
        $subject = $config['subject'] ?? null;
        if (! is_string($subject) || (! str_starts_with($subject, 'mailto:') && ! str_starts_with($subject, 'https://'))) {
            $errors[] = 'subject must be a mailto: or https: URI';
        }

        $publicKey = config('notifications.web_push.public_key');
        $privateKey = config('notifications.web_push.private_key');
        if (empty($publicKey) || empty($privateKey)) {
            $errors[] = 'VAPID keypair missing — run jabali:notifications:vapid to generate';
        }

        return $errors;
    }

    public function send(NotificationChannel $channel, NotificationPayload $payload): ChannelResult
    {
        $config = $channel->config;
        $errors = $this->validateConfig($config);
        if ($errors !== []) {
            return ChannelResult::skipped(implode('; ', $errors));
        }

        if (! class_exists(WebPush::class)) {
            return ChannelResult::skipped('minishlink/web-push package not installed');
        }

        $subs = PushSubscription::query()->get();
        if ($subs->isEmpty()) {
            return ChannelResult::skipped('no active browser subscriptions');
        }

        try {
            $auth = [
                'VAPID' => [
                    'subject' => $config['subject'],
                    'publicKey' => (string) config('notifications.web_push.public_key'),
                    'privateKey' => (string) config('notifications.web_push.private_key'),
                ],
            ];

            $webPush = new WebPush($auth);
            $pushPayload = json_encode([
                'title' => "[Jabali] {$payload->subject}",
                'body' => $payload->message,
                'severity' => $payload->severity,
                'type' => $payload->type,
                'ts' => now()->toIso8601String(),
            ], JSON_THROW_ON_ERROR);

            $recipients = [];
            foreach ($subs as $sub) {
                $webPush->queueNotification(
                    MinishlinkSubscription::create([
                        'endpoint' => $sub->endpoint,
                        'keys' => ['p256dh' => $sub->p256dh, 'auth' => $sub->auth],
                    ]),
                    $pushPayload
                );
                $recipients[] = $sub->endpoint;
            }

            $delivered = 0;
            foreach ($webPush->flush() as $report) {
                if ($report->isSuccess()) {
                    $delivered++;
                } elseif ($report->isSubscriptionExpired()) {
                    // 410 Gone / 404 — prune the dead subscription.
                    PushSubscription::query()
                        ->where('endpoint', $report->getRequest()->getUri()->__toString())
                        ->delete();
                }
            }

            if ($delivered === 0) {
                return ChannelResult::failed('all web push subscriptions failed delivery');
            }

            return ChannelResult::sent($recipients);
        } catch (Throwable $e) {
            Log::warning('Notifications/WebPushDriver failed', ['error' => $e->getMessage()]);

            return ChannelResult::failed($e->getMessage());
        }
    }
}
