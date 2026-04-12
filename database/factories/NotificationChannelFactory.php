<?php

declare(strict_types=1);

namespace Database\Factories;

use App\Models\NotificationChannel;
use Illuminate\Database\Eloquent\Factories\Factory;

/**
 * @extends Factory<NotificationChannel>
 */
class NotificationChannelFactory extends Factory
{
    /**
     * @return array<string, mixed>
     */
    public function definition(): array
    {
        return [
            'name' => $this->faker->unique()->slug(2),
            'type' => 'email',
            'config' => ['recipients' => [$this->faker->safeEmail()]],
            'enabled' => true,
        ];
    }

    public function email(array $recipients = ['ops@example.com']): self
    {
        return $this->state(fn () => [
            'type' => 'email',
            'config' => ['recipients' => $recipients],
        ]);
    }

    public function telegram(string $botToken = 'test-token', string $chatId = '123456'): self
    {
        return $this->state(fn () => [
            'type' => 'telegram',
            'config' => ['bot_token' => $botToken, 'chat_id' => $chatId],
        ]);
    }

    public function ntfy(string $url = 'https://ntfy.sh/jabali-test'): self
    {
        return $this->state(fn () => [
            'type' => 'ntfy',
            'config' => ['url' => $url, 'priority' => 'default'],
        ]);
    }

    public function webPush(): self
    {
        return $this->state(fn () => [
            'type' => 'web_push',
            'config' => ['subject' => 'mailto:admin@localhost'],
        ]);
    }

    public function disabled(): self
    {
        return $this->state(fn () => ['enabled' => false]);
    }
}
