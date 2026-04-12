<?php

declare(strict_types=1);

namespace App\Notifications\Channels;

use App\Models\NotificationChannel;
use App\Notifications\NotificationPayload;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Facades\Mail;
use Throwable;

/**
 * SMTP / Laravel Mail delivery. Config:
 *   ['recipients' => ['ops@example.com', ...]]
 */
final class EmailDriver implements ChannelDriver
{
    public static function type(): string
    {
        return 'email';
    }

    public function validateConfig(array $config): array
    {
        $errors = [];

        $recipients = $config['recipients'] ?? null;
        if (! is_array($recipients) || $recipients === []) {
            $errors[] = 'recipients must be a non-empty array of email addresses';

            return $errors;
        }

        foreach ($recipients as $i => $r) {
            if (! is_string($r) || filter_var($r, FILTER_VALIDATE_EMAIL) === false) {
                $errors[] = "recipients[{$i}] is not a valid email address";
            }
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

        /** @var array<int, string> $recipients */
        $recipients = $config['recipients'];

        try {
            $hostname = $payload->hostname ?? gethostname();
            $sender = "webmaster@{$hostname}";

            Mail::raw($payload->toPlainText(), function ($mail) use ($recipients, $sender, $payload, $hostname) {
                $mail->from($sender, "Jabali Panel ({$hostname})");
                $mail->to($recipients);
                $mail->subject("[Jabali] {$payload->subject}");
            });

            return ChannelResult::sent($recipients);
        } catch (Throwable $e) {
            Log::warning('Notifications/EmailDriver failed', ['error' => $e->getMessage()]);

            return ChannelResult::failed($e->getMessage());
        }
    }
}
