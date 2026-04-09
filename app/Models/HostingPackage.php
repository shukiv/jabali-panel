<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;

class HostingPackage extends Model
{
    use HasFactory;

    protected $fillable = [
        'name',
        'description',
        'disk_quota_mb',
        'bandwidth_gb',
        'domains_limit',
        'databases_limit',
        'mailboxes_limit',
        'is_active',
        'ssh_shell_enabled',
        'ssh_isolation_mode',
        'cpu_quota',
        'memory_limit_mb',
        'io_read_mbps',
        'io_write_mbps',
        'max_processes',
    ];

    protected function casts(): array
    {
        return [
            'is_active' => 'boolean',
            'ssh_shell_enabled' => 'boolean',
        ];
    }

    public function users(): HasMany
    {
        return $this->hasMany(User::class);
    }
}
