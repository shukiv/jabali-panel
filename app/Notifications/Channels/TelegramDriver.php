<?php

declare(strict_types=1);

namespace App\Notifications\Channels;

use App\Models\NotificationChannel;
use App\Notifications\NotificationPayload;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Facades\Log;
use Throwable;

/**
 * Telegram Bot API driver. Config:
 *   ['bot_token' => '<token>', 'chat_id' => '<chat id or @channel>']
 *
 * Rolled as a thin curl/Http client rather than pulling in a community
 * package — fewer deps, tighter surface, and the Bot API is dead simple.
 */
final class TelegramDriver implements ChannelDriver
{
    public static function type(): string
    {
        return 'telegram';
    }

    public function validateConfig(array $config): array
    {
        $errors = [];
        $token = $config['bot_token'] ?? null;
        $chatId = $config['chat_id'] ?? null;

        if (! is_string($token) || ! preg_match('/^\d+:[A-Za-z0-9_-]{30,}$/', $token)) {
            $errors[] = 'bot_token must be a valid Telegram bot token (e.g. 1234567890:AA…)';
        }
        if (! is_string($chatId) || $chatId === '') {
            $errors[] = 'chat_id must be a non-empty string';
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

        /** @var string $token */
        $token = $config['bot_token'];
        /** @var string $chatId */
        $chatId = $config['chat_id'];

        // Telegram caps messages at 4096 chars — trim with an ellipsis.
        $body = "*[Jabali] {$payload->subject}*\n\n".$payload->toPlainText();
        if (mb_strlen($body) > 4000) {
            $body = mb_substr($body, 0, 3990)."\n… (truncated)";
        }

        try {
            $response = Http::timeout(10)
                ->asForm()
                ->post("https://api.telegram.org/bot{$token}/sendMessage", [
                    'chat_id' => $chatId,
                    'text' => $body,
                    'parse_mode' => 'Markdown',
                    'disable_web_page_preview' => true,
                ]);

            if ($response->successful() && ($response->json('ok') === true)) {
                return ChannelResult::sent([$chatId]);
            }

            $err = $response->json('description') ?? "HTTP {$response->status()}";

            return ChannelResult::failed("Telegram API: {$err}");
        } catch (Throwable $e) {
            Log::warning('Notifications/TelegramDriver failed', ['error' => $e->getMessage()]);

            return ChannelResult::failed($e->getMessage());
        }
    }
}
