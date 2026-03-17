<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

class CronJob extends Model
{
    protected $fillable = [
        'user_id',
        'name',
        'schedule',
        'command',
        'type',
        'metadata',
        'is_active',
        'last_run_at',
        'last_run_status',
        'last_run_output',
    ];

    protected function casts(): array
    {

        return [
            'metadata' => 'array',
            'is_active' => 'boolean',
            'last_run_at' => 'datetime',
        ];

    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function getScheduleHumanAttribute(): string
    {
        $parts = explode(' ', $this->schedule);
        if (count($parts) !== 5) {
            return $this->schedule;
        }

        [$minute, $hour, $day, $month, $weekday] = $parts;

        // Common patterns
        if ($this->schedule === '* * * * *') {
            return 'Every minute';
        }
        if ($this->schedule === '*/5 * * * *') {
            return 'Every 5 minutes';
        }
        if ($this->schedule === '*/10 * * * *') {
            return 'Every 10 minutes';
        }
        if ($this->schedule === '*/15 * * * *') {
            return 'Every 15 minutes';
        }
        if ($this->schedule === '*/30 * * * *') {
            return 'Every 30 minutes';
        }
        if ($this->schedule === '0 * * * *') {
            return 'Every hour';
        }
        if ($this->schedule === '0 */2 * * *') {
            return 'Every 2 hours';
        }
        if ($this->schedule === '0 */6 * * *') {
            return 'Every 6 hours';
        }
        if ($this->schedule === '0 */12 * * *') {
            return 'Every 12 hours';
        }
        if ($this->schedule === '0 0 * * *') {
            return 'Daily at midnight';
        }
        if ($this->schedule === '0 0 * * 0') {
            return 'Weekly on Sunday';
        }
        if ($this->schedule === '0 0 1 * *') {
            return 'Monthly on the 1st';
        }

        return $this->schedule;
    }

    public static function scheduleOptions(): array
    {
        return [
            '* * * * *' => 'Every minute',
            '*/5 * * * *' => 'Every 5 minutes',
            '*/10 * * * *' => 'Every 10 minutes',
            '*/15 * * * *' => 'Every 15 minutes',
            '*/30 * * * *' => 'Every 30 minutes',
            '0 * * * *' => 'Every hour',
            '0 */2 * * *' => 'Every 2 hours',
            '0 */6 * * *' => 'Every 6 hours',
            '0 */12 * * *' => 'Every 12 hours',
            '0 0 * * *' => 'Daily at midnight',
            '0 0 * * 0' => 'Weekly on Sunday',
            '0 0 1 * *' => 'Monthly on the 1st',
        ];
    }
}
