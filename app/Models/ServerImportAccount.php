<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

class ServerImportAccount extends Model
{
    protected $fillable = [
        'server_import_id',
        'source_username',
        'target_username',
        'email',
        'main_domain',
        'addon_domains',
        'subdomains',
        'databases',
        'email_accounts',
        'disk_usage',
        'status',
        'progress',
        'current_task',
        'import_log',
        'error',
    ];

    protected $casts = [
        'addon_domains' => 'array',
        'subdomains' => 'array',
        'databases' => 'array',
        'email_accounts' => 'array',
        'import_log' => 'array',
    ];

    public function serverImport(): BelongsTo
    {
        return $this->belongsTo(ServerImport::class);
    }

    public function addLog(string $message): void
    {
        $log = $this->import_log ?? [];
        $log[] = [
            'time' => now()->toDateTimeString(),
            'message' => $message,
        ];
        $this->update(['import_log' => $log]);
    }

    public function getFormattedDiskUsageAttribute(): string
    {
        $bytes = $this->disk_usage;
        if ($bytes >= 1073741824) {
            return number_format($bytes / 1073741824, 2) . ' GB';
        } elseif ($bytes >= 1048576) {
            return number_format($bytes / 1048576, 2) . ' MB';
        } elseif ($bytes >= 1024) {
            return number_format($bytes / 1024, 2) . ' KB';
        }
        return $bytes . ' B';
    }

    public function getStatusColorAttribute(): string
    {
        return match ($this->status) {
            'pending' => 'gray',
            'importing' => 'info',
            'completed' => 'success',
            'failed' => 'danger',
            'skipped' => 'warning',
            default => 'gray',
        };
    }

    public function getDomainCountAttribute(): int
    {
        return 1 + count($this->addon_domains ?? []) + count($this->subdomains ?? []);
    }

    public function getDatabaseCountAttribute(): int
    {
        return count($this->databases ?? []);
    }

    public function getEmailCountAttribute(): int
    {
        return count($this->email_accounts ?? []);
    }
}
