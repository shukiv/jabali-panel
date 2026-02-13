<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

class BackupDestination extends Model
{
    use HasFactory;

    protected $fillable = [
        'user_id',
        'name',
        'type',
        'config',
        'is_default',
        'is_active',
        'is_server_backup',
        'last_tested_at',
        'test_status',
        'test_message',
    ];

    protected function casts(): array
    {
        return [
            'config' => 'encrypted:array',
            'is_default' => 'boolean',
            'is_active' => 'boolean',
            'is_server_backup' => 'boolean',
            'last_tested_at' => 'datetime',
        ];
    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function backups(): HasMany
    {
        return $this->hasMany(Backup::class, 'destination_id');
    }

    public function schedules(): HasMany
    {
        return $this->hasMany(BackupSchedule::class, 'destination_id');
    }

    /**
     * Check if destination is local storage.
     */
    public function isLocal(): bool
    {
        return $this->type === 'local';
    }

    /**
     * Check if destination is remote storage.
     */
    public function isRemote(): bool
    {
        return in_array($this->type, ['sftp', 'nfs', 's3']);
    }

    /**
     * Get the display label for the destination type.
     */
    public function getTypeLabelAttribute(): string
    {
        return match ($this->type) {
            'local' => 'Local Storage',
            'sftp' => 'SFTP Server',
            'nfs' => 'NFS Mount',
            's3' => 'S3-Compatible Storage',
            default => ucfirst($this->type),
        };
    }

    /**
     * Get config value by key.
     */
    public function getConfigValue(string $key, mixed $default = null): mixed
    {
        return $this->config[$key] ?? $default;
    }

    /**
     * Scope for active destinations.
     */
    public function scopeActive($query)
    {
        return $query->where('is_active', true);
    }

    /**
     * Scope for user destinations.
     */
    public function scopeForUser($query, int $userId)
    {
        return $query->where('user_id', $userId);
    }

    /**
     * Scope for server backup destinations (admin-level).
     */
    public function scopeServerBackups($query)
    {
        return $query->where('is_server_backup', true);
    }
}
