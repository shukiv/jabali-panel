<?php

declare(strict_types=1);

namespace Tests\Unit\Notifications;

use App\Models\NotificationChannel;
use App\Models\NotificationTypeChannel;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\DB;
use Tests\TestCase;

class NotificationChannelTest extends TestCase
{
    use RefreshDatabase;

    public function test_config_is_stored_encrypted_and_round_trips_on_read(): void
    {
        $channel = NotificationChannel::factory()->telegram('secret-bot-token', '42')->create();

        // Model-level decryption gives us the original array.
        $fresh = NotificationChannel::find($channel->id);
        $this->assertSame('secret-bot-token', $fresh->config['bot_token']);
        $this->assertSame('42', $fresh->config['chat_id']);

        // Raw column must not contain the plaintext — it's encrypted at rest.
        $raw = DB::table('notification_channels')->where('id', $channel->id)->value('config');
        $this->assertIsString($raw);
        $this->assertStringNotContainsString('secret-bot-token', $raw);
        $this->assertStringNotContainsString('"chat_id"', $raw);
    }

    public function test_types_constant_matches_enum(): void
    {
        $this->assertSame(['email', 'telegram', 'ntfy', 'web_push'], NotificationChannel::TYPES);
    }

    public function test_factory_states_produce_valid_channels(): void
    {
        $this->assertSame('email', NotificationChannel::factory()->email()->make()->type);
        $this->assertSame('telegram', NotificationChannel::factory()->telegram()->make()->type);
        $this->assertSame('ntfy', NotificationChannel::factory()->ntfy()->make()->type);
        $this->assertSame('web_push', NotificationChannel::factory()->webPush()->make()->type);
    }

    public function test_severity_routes_relation_returns_type_channels(): void
    {
        $channel = NotificationChannel::factory()->email()->create();
        NotificationTypeChannel::factory()->critical()->create(['channel_id' => $channel->id]);
        NotificationTypeChannel::factory()->warning()->create(['channel_id' => $channel->id]);

        $this->assertCount(2, $channel->severityRoutes);
    }

    public function test_enabled_is_cast_to_bool(): void
    {
        $channel = NotificationChannel::factory()->disabled()->create();
        $this->assertFalse($channel->enabled);
        $this->assertIsBool($channel->enabled);
    }
}
