<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;

/**
 * A configured destination for admin notifications — email recipient list,
 * Telegram bot+chat, ntfy topic, or the shared Web Push VAPID settings.
 *
 * The `config` column is stored as encrypted JSON via `encrypted:array`.
 * Its shape depends on `type`:
 *
 * - email:    ['recipients' => ['ops@example.com', ...]]
 * - telegram: ['bot_token' => '...', 'chat_id' => '...']
 * - ntfy:     ['url' => 'https://ntfy.sh/jabali-alerts', 'priority' => 'default']
 * - web_push: ['subject' => 'mailto:...'] — per-admin browser subscriptions
 *             live in the push_subscriptions table (Phase 5), not here.
 *
 * @property int $id
 * @property string $name
 * @property string $type
 * @property array<string, mixed> $config
 * @property bool $enabled
 * @property \Illuminate\Support\Carbon|null $created_at
 * @property \Illuminate\Support\Carbon|null $updated_at
 */
class NotificationChannel extends Model
{
    use HasFactory;

    public const TYPES = ['email', 'telegram', 'ntfy', 'web_push'];

    protected $fillable = [
        'name',
        'type',
        'config',
        'enabled',
    ];

    /**
     * @return array<string, string>
     */
    protected function casts(): array
    {
        return [
            'config' => 'encrypted:array',
            'enabled' => 'boolean',
        ];
    }

    /**
     * @return HasMany<NotificationTypeChannel, $this>
     */
    public function severityRoutes(): HasMany
    {
        return $this->hasMany(NotificationTypeChannel::class, 'channel_id');
    }
}
