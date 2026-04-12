<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * Routing rule — "send severity X to channel Y". A channel may appear at
 * most once per severity (enforced at the DB by uniq_severity_channel).
 *
 * @property int $id
 * @property string $severity
 * @property int $channel_id
 * @property int $priority
 * @property \Illuminate\Support\Carbon|null $created_at
 * @property \Illuminate\Support\Carbon|null $updated_at
 */
class NotificationTypeChannel extends Model
{
    use HasFactory;

    public const SEVERITIES = ['info', 'warning', 'critical'];

    protected $fillable = [
        'severity',
        'channel_id',
        'priority',
    ];

    /**
     * @return array<string, string>
     */
    protected function casts(): array
    {
        return [
            'priority' => 'integer',
        ];
    }

    /**
     * @return BelongsTo<NotificationChannel, $this>
     */
    public function channel(): BelongsTo
    {
        return $this->belongsTo(NotificationChannel::class, 'channel_id');
    }
}
