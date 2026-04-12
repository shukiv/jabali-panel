<?php

declare(strict_types=1);

namespace App\Notifications\Channels;

use App\Models\NotificationChannel;
use App\Notifications\NotificationPayload;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Facades\Log;
use Throwable;

/**
 * ntfy.sh driver. Config:
 *   ['url' => 'https://ntfy.sh/<topic>' | 'https://self-hosted.ntfy.local/<topic>',
 *    'priority' => 'min' | 'low' | 'default' | 'high' | 'urgent',
 *    'token' => '<optional bearer token for protected topics>']
 *
 * Severity drives priority when no explicit priority is set:
 *   info -> 'default', warning -> 'high', critical -> 'urgent'
 */
final class NtfyDriver implements ChannelDriver
{
    private const VALID_PRIORITIES = ['min', 'low', 'default', 'high', 'urgent'];

    public static function type(): string
    {
        return 'ntfy';
    }

    public function validateConfig(array $config): array
    {
        $errors = [];
        $url = $config['url'] ?? null;
        if (! is_string($url) || filter_var($url, FILTER_VALIDATE_URL) === false) {
            $errors[] = 'url must be a valid https URL pointing at an ntfy topic';
        }
        if (isset($config['priority']) && ! in_array($config['priority'], self::VALID_PRIORITIES, true)) {
            $errors[] = 'priority must be one of: '.implode(', ', self::VALID_PRIORITIES);
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

        $priority = $config['priority'] ?? match ($payload->severity) {
            'critical' => 'urgent',
            'warning' => 'high',
            default => 'default',
        };

        $tags = ['jabali', $payload->severity];

        try {
            $request = Http::timeout(10)
                ->withHeaders([
                    'Title' => "[Jabali] {$payload->subject}",
                    'Priority' => $priority,
                    'Tags' => implode(',', $tags),
                ]);

            if (! empty($config['token'])) {
                $request = $request->withToken($config['token']);
            }

            $response = $request->withBody(
                $payload->toPlainText(),
                'text/plain'
            )->post($config['url']);

            if ($response->successful()) {
                return ChannelResult::sent([$config['url']]);
            }

            return ChannelResult::failed("ntfy HTTP {$response->status()}: ".$response->body());
        } catch (Throwable $e) {
            Log::warning('Notifications/NtfyDriver failed', ['error' => $e->getMessage()]);

            return ChannelResult::failed($e->getMessage());
        }
    }
}
