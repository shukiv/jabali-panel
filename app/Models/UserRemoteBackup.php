<?php

declare(strict_types=1);

namespace App\Models;

use Carbon\Carbon;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

class UserRemoteBackup extends Model
{
    protected $fillable = [
        'user_id',
        'destination_id',
        'backup_name',
        'backup_path',
        'backup_type',
        'backup_date',
        'indexed_at',
    ];

    protected function casts(): array
    {
        return [
            'backup_date' => 'datetime',
            'indexed_at' => 'datetime',
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

    /**
     * Parse backup date from backup name (e.g., 2026-01-19_230002 -> 2026-01-19 23:00:02)
     */
    public static function parseBackupDate(string $backupName): ?Carbon
    {
        if (preg_match('/^(\d{4}-\d{2}-\d{2})_(\d{2})(\d{2})(\d{2})$/', $backupName, $matches)) {
            return Carbon::parse("{$matches[1]} {$matches[2]}:{$matches[3]}:{$matches[4]}");
        }

        return null;
    }

    /**
     * Get formatted backup date for display.
     */
    public function getFormattedDateAttribute(): string
    {
        return $this->backup_date?->format('M j, Y H:i') ?? $this->backup_name;
    }

    /**
     * Scope for a specific user.
     */
    public function scopeForUser($query, int $userId)
    {
        return $query->where('user_id', $userId);
    }

    /**
     * Scope for a specific destination.
     */
    public function scopeForDestination($query, int $destinationId)
    {
        return $query->where('destination_id', $destinationId);
    }
}
