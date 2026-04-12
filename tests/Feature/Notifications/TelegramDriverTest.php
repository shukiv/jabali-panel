<?php

declare(strict_types=1);

namespace Tests\Feature\Notifications;

use App\Models\NotificationChannel;
use App\Notifications\Channels\TelegramDriver;
use App\Notifications\NotificationPayload;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

class TelegramDriverTest extends TestCase
{
    use RefreshDatabase;

    public function test_validates_bot_token_shape(): void
    {
        $driver = new TelegramDriver;
        $this->assertNotEmpty($driver->validateConfig(['bot_token' => 'junk', 'chat_id' => '1']));
        $this->assertEmpty($driver->validateConfig([
            'bot_token' => '1234567890:AAHnoiseHnoiseHnoiseHnoiseHnoiseHnoise',
            'chat_id' => '1',
        ]));
    }

    public function test_posts_to_telegram_api_on_success(): void
    {
        Http::fake(['api.telegram.org/*' => Http::response(['ok' => true, 'result' => []], 200)]);

        $channel = NotificationChannel::factory()
            ->telegram('1234567890:AAHnoiseHnoiseHnoiseHnoiseHnoiseHnoise', '42')
            ->create();
        $payload = new NotificationPayload('test', 'info', 'hi', 'body', [], 'host');

        $result = (new TelegramDriver)->send($channel, $payload);

        $this->assertTrue($result->success);
    }

    public function test_non_ok_response_is_failed(): void
    {
        Http::fake(['api.telegram.org/*' => Http::response(['ok' => false, 'description' => 'chat not found'], 400)]);

        $channel = NotificationChannel::factory()
            ->telegram('1234567890:AAHnoiseHnoiseHnoiseHnoiseHnoiseHnoise', '42')
            ->create();
        $payload = new NotificationPayload('t', 'info', 's', 'm');

        $result = (new TelegramDriver)->send($channel, $payload);

        $this->assertFalse($result->success);
        $this->assertStringContainsString('chat not found', (string) $result->error);
    }
}
