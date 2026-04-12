<?php

declare(strict_types=1);

namespace Database\Factories;

use App\Models\NotificationChannel;
use App\Models\NotificationTypeChannel;
use Illuminate\Database\Eloquent\Factories\Factory;

/**
 * @extends Factory<NotificationTypeChannel>
 */
class NotificationTypeChannelFactory extends Factory
{
    /**
     * @return array<string, mixed>
     */
    public function definition(): array
    {
        return [
            'severity' => $this->faker->randomElement(NotificationTypeChannel::SEVERITIES),
            'channel_id' => NotificationChannel::factory(),
            'priority' => 0,
        ];
    }

    public function info(): self
    {
        return $this->state(fn () => ['severity' => 'info']);
    }

    public function warning(): self
    {
        return $this->state(fn () => ['severity' => 'warning']);
    }

    public function critical(): self
    {
        return $this->state(fn () => ['severity' => 'critical']);
    }
}
