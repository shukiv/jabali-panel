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
    ];

    protected $casts = [
        'is_active' => 'boolean',
    ];

    public function users(): HasMany
    {
        return $this->hasMany(User::class);
    }
}
