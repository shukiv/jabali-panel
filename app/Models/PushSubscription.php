<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * One row per browser-installed Web Push subscription. Admin opens the
 * Notifications page, clicks "Enable browser push", browser prompts,
 * the resulting PushSubscriptionJSON is POSTed here and persisted.
 *
 * @property int $id
 * @property int $admin_user_id
 * @property string $endpoint
 * @property string $p256dh
 * @property string $auth
 * @property string|null $user_agent
 * @property \Illuminate\Support\Carbon|null $last_used_at
 */
class PushSubscription extends Model
{
    use HasFactory;

    protected $fillable = [
        'admin_user_id',
        'endpoint',
        'p256dh',
        'auth',
        'user_agent',
        'last_used_at',
    ];

    protected function casts(): array
    {
        return [
            'last_used_at' => 'datetime',
        ];
    }

    /**
     * @return BelongsTo<User, $this>
     */
    public function adminUser(): BelongsTo
    {
        return $this->belongsTo(User::class, 'admin_user_id');
    }
}
