<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;

class ServerImport extends Model
{
    protected $fillable = [
        'name',
        'source_type',
        'import_method',
        'backup_path',
        'remote_host',
        'remote_port',
        'remote_user',
        'remote_password',
        'remote_api_token',
        'discovered_accounts',
        'selected_accounts',
        'import_options',
        'status',
        'progress',
        'current_task',
        'import_log',
        'errors',
        'started_at',
        'completed_at',
    ];

    protected $casts = [
        'discovered_accounts' => 'array',
        'selected_accounts' => 'array',
        'import_options' => 'array',
        'import_log' => 'array',
        'errors' => 'array',
        'started_at' => 'datetime',
        'completed_at' => 'datetime',
        'remote_password' => 'encrypted',
        'remote_api_token' => 'encrypted',
    ];

    public function accounts(): HasMany
    {
        return $this->hasMany(ServerImportAccount::class);
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

    public function addError(string $error): void
    {
        $errors = $this->errors ?? [];
        $errors[] = [
            'time' => now()->toDateTimeString(),
            'error' => $error,
        ];
        $this->update(['errors' => $errors]);
    }

    public function getStatusColorAttribute(): string
    {
        return match ($this->status) {
            'pending' => 'gray',
            'discovering' => 'info',
            'ready' => 'warning',
            'importing' => 'info',
            'completed' => 'success',
            'failed' => 'danger',
            'cancelled' => 'gray',
            default => 'gray',
        };
    }

    public function getCompletedAccountsCountAttribute(): int
    {
        return $this->accounts()->where('status', 'completed')->count();
    }

    public function getTotalAccountsCountAttribute(): int
    {
        return $this->accounts()->count();
    }
}
