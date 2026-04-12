<?php

declare(strict_types=1);

namespace Tests\Unit\Notifications;

use App\Models\NotificationChannel;
use App\Models\NotificationTypeChannel;
use Illuminate\Database\QueryException;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class NotificationTypeChannelTest extends TestCase
{
    use RefreshDatabase;

    public function test_channel_belongs_to_notification_channel(): void
    {
        $channel = NotificationChannel::factory()->email()->create();
        $route = NotificationTypeChannel::factory()->critical()->create(['channel_id' => $channel->id]);

        $this->assertSame($channel->id, $route->channel->id);
    }

    public function test_severity_channel_pair_is_unique(): void
    {
        $channel = NotificationChannel::factory()->email()->create();
        NotificationTypeChannel::factory()->warning()->create(['channel_id' => $channel->id]);

        $this->expectException(QueryException::class);
        NotificationTypeChannel::factory()->warning()->create(['channel_id' => $channel->id]);
    }

    public function test_different_severities_on_same_channel_are_allowed(): void
    {
        $channel = NotificationChannel::factory()->email()->create();
        NotificationTypeChannel::factory()->info()->create(['channel_id' => $channel->id]);
        NotificationTypeChannel::factory()->warning()->create(['channel_id' => $channel->id]);
        NotificationTypeChannel::factory()->critical()->create(['channel_id' => $channel->id]);

        $this->assertDatabaseCount('notification_type_channels', 3);
    }

    public function test_deleting_a_channel_cascades_to_its_routes(): void
    {
        $channel = NotificationChannel::factory()->email()->create();
        NotificationTypeChannel::factory()->critical()->create(['channel_id' => $channel->id]);
        NotificationTypeChannel::factory()->warning()->create(['channel_id' => $channel->id]);

        $channel->delete();

        $this->assertDatabaseCount('notification_type_channels', 0);
    }

    public function test_severities_constant_exposes_enum_values(): void
    {
        $this->assertSame(['info', 'warning', 'critical'], NotificationTypeChannel::SEVERITIES);
    }
}
