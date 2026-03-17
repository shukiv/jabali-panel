<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

class BackupRestore extends Model
{
    use HasFactory;

    protected $fillable = [
        'backup_id',
        'user_id',
        'restore_files',
        'restore_databases',
        'restore_mailboxes',
        'restore_dns',
        'selected_domains',
        'selected_databases',
        'selected_mailboxes',
        'status',
        'progress',
        'log',
        'started_at',
        'completed_at',
        'error_message',
        'result',
    ];

    protected function casts(): array
    {
        return [
            'restore_files' => 'boolean',
            'restore_databases' => 'boolean',
            'restore_mailboxes' => 'boolean',
            'restore_dns' => 'boolean',
            'selected_domains' => 'array',
            'selected_databases' => 'array',
            'selected_mailboxes' => 'array',
            'progress' => 'integer',
            'result' => 'array',
            'started_at' => 'datetime',
            'completed_at' => 'datetime',
        ];
    }

    public function backup(): BelongsTo
    {
        return $this->belongsTo(Backup::class);
    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    /**
     * Get duration in human-readable format.
     */
    public function getDurationAttribute(): ?string
    {
        if (! $this->started_at || ! $this->completed_at) {
            return null;
        }

        $seconds = $this->completed_at->diffInSeconds($this->started_at);

        if ($seconds >= 3600) {
            return gmdate('H:i:s', $seconds);
        } elseif ($seconds >= 60) {
            return gmdate('i:s', $seconds).' min';
        }

        return $seconds.' sec';
    }

    /**
     * Append a log message.
     */
    public function appendLog(string $message): void
    {
        $timestamp = now()->format('H:i:s');
        $this->log = ($this->log ?? '')."[{$timestamp}] {$message}\n";
        $this->save();
    }

    /**
     * Update progress.
     */
    public function updateProgress(int $progress, ?string $message = null): void
    {
        $this->progress = min(100, max(0, $progress));

        if ($message) {
            $this->appendLog($message);
        } else {
            $this->save();
        }
    }

    /**
     * Mark as running.
     */
    public function markAsRunning(): void
    {
        $this->status = 'running';
        $this->started_at = now();
        $this->save();
    }

    /**
     * Mark as completed.
     */
    public function markAsCompleted(array $result = []): void
    {
        $this->status = 'completed';
        $this->progress = 100;
        $this->completed_at = now();
        $this->result = $result;
        $this->save();
    }

    /**
     * Mark as failed.
     */
    public function markAsFailed(string $error): void
    {
        $this->status = 'failed';
        $this->completed_at = now();
        $this->error_message = $error;
        $this->appendLog("ERROR: {$error}");
    }

    /**
     * Scope for running restores.
     */
    public function scopeRunning($query)
    {
        return $query->whereIn('status', ['pending', 'downloading', 'running']);
    }

    /**
     * Scope for user restores.
     */
    public function scopeForUser($query, int $userId)
    {
        return $query->where('user_id', $userId);
    }

    /**
     * Get status color for UI.
     */
    public function getStatusColorAttribute(): string
    {
        return match ($this->status) {
            'completed' => 'success',
            'failed' => 'danger',
            'running', 'downloading' => 'warning',
            'pending' => 'gray',
            default => 'gray',
        };
    }

    /**
     * Get status label for UI.
     */
    public function getStatusLabelAttribute(): string
    {
        return match ($this->status) {
            'pending' => 'Pending',
            'downloading' => 'Downloading...',
            'running' => 'Restoring...',
            'completed' => 'Completed',
            'failed' => 'Failed',
            default => ucfirst($this->status),
        };
    }
}
