<?php

declare(strict_types=1);

namespace App\Models;

use App\Support\ServerFacts;
use Carbon\Carbon;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

class BackupSchedule extends Model
{
    use HasFactory;

    protected $fillable = [
        'user_id',
        'destination_id',
        'name',
        'is_active',
        'is_server_backup',
        'frequency',
        'time',
        'day_of_week',
        'day_of_month',
        'include_files',
        'include_databases',
        'include_mailboxes',
        'include_dns',
        'domains',
        'databases',
        'mailboxes',
        'users',
        'retention_count',
        'last_run_at',
        'next_run_at',
        'last_status',
        'last_error',
        'metadata',
    ];

    protected function casts(): array
    {
        return [
            'is_active' => 'boolean',
            'is_server_backup' => 'boolean',
            'include_files' => 'boolean',
            'include_databases' => 'boolean',
            'include_mailboxes' => 'boolean',
            'include_dns' => 'boolean',
            'domains' => 'array',
            'databases' => 'array',
            'mailboxes' => 'array',
            'users' => 'array',
            'metadata' => 'array',
            'retention_count' => 'integer',
            'day_of_week' => 'integer',
            'day_of_month' => 'integer',
            'last_run_at' => 'datetime',
            'next_run_at' => 'datetime',
        ];
    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function destination(): BelongsTo
    {
        return $this->belongsTo(BackupDestination::class, 'destination_id');
    }

    public function backups(): HasMany
    {
        return $this->hasMany(Backup::class, 'schedule_id');
    }

    /**
     * Check if the schedule should run now.
     */
    public function shouldRun(): bool
    {
        if (! $this->is_active) {
            return false;
        }

        if (! $this->next_run_at) {
            return true;
        }

        return $this->next_run_at->isPast();
    }

    /**
     * Calculate and set the next run time.
     */
    public function calculateNextRun(): Carbon
    {
        $timezone = $this->getSystemTimezone();
        $now = Carbon::now($timezone);
        $time = explode(':', $this->time);
        $hour = (int) ($time[0] ?? 2);
        $minute = (int) ($time[1] ?? 0);

        $next = $now->copy()->setTime($hour, $minute, 0);

        // If time already passed today, start from tomorrow
        if ($next->isPast()) {
            $next->addDay();
        }

        switch ($this->frequency) {
            case 'hourly':
                $next = $now->copy()->addHour()->startOfHour();
                break;

            case 'daily':
                // Already set to next occurrence
                break;

            case 'weekly':
                $targetDay = $this->day_of_week ?? 0; // Default to Sunday
                while ($next->dayOfWeek !== $targetDay) {
                    $next->addDay();
                }
                break;

            case 'monthly':
                $targetDay = $this->day_of_month ?? 1;
                $next->day = min($targetDay, $next->daysInMonth);
                if ($next->isPast()) {
                    $next->addMonth();
                    $next->day = min($targetDay, $next->daysInMonth);
                }
                break;
        }

        $nextUtc = $next->copy()->setTimezone('UTC');
        $this->attributes['next_run_at'] = $nextUtc->format($this->getDateFormat());

        return $nextUtc;
    }

    /**
     * Get frequency label for UI.
     */
    public function getFrequencyLabelAttribute(): string
    {
        $base = match ($this->frequency) {
            'hourly' => 'Every hour',
            'daily' => 'Daily at '.$this->time,
            'weekly' => 'Weekly on '.$this->getDayName().' at '.$this->time,
            'monthly' => 'Monthly on day '.($this->day_of_month ?? 1).' at '.$this->time,
            default => ucfirst($this->frequency),
        };

        return $base;
    }

    /**
     * Get day name for weekly schedules.
     */
    protected function getDayName(): string
    {
        $days = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];

        return $days[$this->day_of_week ?? 0];
    }

    protected function getSystemTimezone(): string
    {
        static $timezone = null;
        if ($timezone === null) {
            $timezone = ServerFacts::timezone();
        }

        return $timezone;
    }

    /**
     * Scope for active schedules.
     */
    public function scopeActive($query)
    {
        return $query->where('is_active', true);
    }

    /**
     * Scope for due schedules.
     */
    public function scopeDue($query)
    {
        return $query->active()
            ->where(function ($q) {
                $q->whereNull('next_run_at')
                    ->orWhere('next_run_at', '<=', now());
            });
    }

    /**
     * Scope for user schedules.
     */
    public function scopeForUser($query, int $userId)
    {
        return $query->where('user_id', $userId);
    }

    /**
     * Scope for server backup schedules.
     */
    public function scopeServerBackups($query)
    {
        return $query->where('is_server_backup', true);
    }

    /**
     * Get last status color for UI.
     */
    public function getLastStatusColorAttribute(): string
    {
        return match ($this->last_status) {
            'success' => 'success',
            'failed' => 'danger',
            default => 'gray',
        };
    }


}
