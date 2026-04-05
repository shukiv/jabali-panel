<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;

class NotificationLog extends Model
{
    protected $fillable = [
        'type',
        'subject',
        'message',
        'context',
        'recipients',
        'status',
        'error',
    ];

    protected function casts(): array
    {
        return [
            'context' => 'array',
            'recipients' => 'array',
        ];
    }

    /**
     * Log a notification.
     */
    public static function log(
        string $type,
        string $subject,
        string $message,
        array $recipients,
        string $status = 'sent',
        ?array $context = null,
        ?string $error = null
    ): self {
        return self::create([
            'type' => $type,
            'subject' => $subject,
            'message' => $message,
            'recipients' => $recipients,
            'status' => $status,
            'context' => $context,
            'error' => $error,
        ]);
    }

    /**
     * Get human-readable type label.
     */
    public function getTypeLabelAttribute(): string
    {
        return match ($this->type) {
            'ssl_errors' => __('SSL Certificate'),
            'disk_quota' => __('Disk Quota'),
            'login_failures' => __('Login Failure'),
            'ssh_logins' => __('SSH Login'),
            'system_updates' => __('System Updates'),
            'service_health' => __('Service Health'),
            'high_load' => __('High Load'),
            'test' => __('Test'),
            default => ucfirst(str_replace('_', ' ', $this->type)),
        };
    }

    /**
     * Scope to get logs from the last N days.
     */
    public function scopeLastDays($query, int $days = 30)
    {
        return $query->where('created_at', '>=', now()->subDays($days));
    }

    /**
     * Clean up old logs.
     */
    public static function cleanup(int $daysToKeep = 30): int
    {
        return self::where('created_at', '<', now()->subDays($daysToKeep))->delete();
    }
}
