<?php

declare(strict_types=1);

namespace App\Models;

use App\Support\Formatter;
use Filament\Models\Contracts\FilamentUser;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Relations\HasMany;
use Illuminate\Foundation\Auth\User as Authenticatable;
use Illuminate\Notifications\Notifiable;
use Laravel\Fortify\TwoFactorAuthenticatable;
use Laravel\Sanctum\HasApiTokens;

class User extends Authenticatable implements FilamentUser
{
    use HasApiTokens, HasFactory, Notifiable, TwoFactorAuthenticatable;

    protected $fillable = [
        'name',
        'username',
        'home_directory',
        'email',
        'password',
        'sftp_password',
        'is_active',
        'hosting_package_id',
        'locale',
        'disk_quota_mb',
        'ssh_isolation_mode',
    ];

    protected $hidden = [
        'password',
        'remember_token',
        'sftp_password',
    ];

    protected function casts(): array
    {
        return [
            'email_verified_at' => 'datetime',
            'password' => 'hashed',
            'is_admin' => 'boolean',
            'is_active' => 'boolean',
            'sftp_password' => 'encrypted',
        ];
    }

    public function getHomeDirectoryAttribute($value): string
    {
        return $value ?? "/home/{$this->username}";
    }

    protected static function booted()
    {
        static::deleting(function ($user) {
            app(\App\Services\UserDeletionService::class)->beforeDelete($user);
        });
    }

    public function isAdmin(): bool
    {
        return (bool) $this->is_admin;
    }

    /**
     * Determine if the user can access the Filament panel.
     */
    public function canAccessPanel(\Filament\Panel $panel): bool
    {
        if ($panel->getId() === 'admin') {
            return $this->is_admin && $this->is_active;
        }

        // Admins can only access the admin panel
        if ($this->is_admin) {
            return false;
        }

        return $this->is_active ?? true;
    }

    /**
     * Get the domains owned by the user.
     */
    public function domains(): HasMany
    {
        return $this->hasMany(Domain::class);
    }

    public function hostingPackage(): \Illuminate\Database\Eloquent\Relations\BelongsTo
    {
        return $this->belongsTo(HostingPackage::class);
    }

    /**
     * Get effective SSH isolation mode (user override or package default).
     */
    public function getEffectiveSshIsolationMode(): string
    {
        return $this->ssh_isolation_mode
            ?? $this->hostingPackage?->ssh_isolation_mode
            ?? 'disabled';
    }

    /**
     * Get disk usage in bytes.
     */
    private ?int $cachedDiskUsageBytes = null;

    public function getDiskUsageBytes(): int
    {
        if ($this->cachedDiskUsageBytes !== null) {
            return $this->cachedDiskUsageBytes;
        }

        // Disk usage must be obtained via the agent (root) to avoid permission-based undercounting.
        try {
            $agent = app(\App\Services\Agent\AgentClient::class);

            $mount = $this->home_directory ?: ("/home/{$this->username}");
            $result = $agent->quotaGet($this->username, $mount);

            if (($result['success'] ?? false) && isset($result['used_mb'])) {
                $this->cachedDiskUsageBytes = (int) ($result['used_mb'] * 1024 * 1024);

                return $this->cachedDiskUsageBytes;
            }
        } catch (\Throwable $e) {
            \Log::warning('Disk usage read failed via agent: '.$e->getMessage(), [
                'username' => $this->username,
            ]);
        }

        $this->cachedDiskUsageBytes = 0;

        return 0;
    }

    /**
     * Get formatted disk usage string.
     */
    public function getDiskUsageFormattedAttribute(): string
    {
        $bytes = $this->getDiskUsageBytes();

        return Formatter::bytes($bytes);
    }

    /**
     * Get quota in bytes.
     */
    public function getQuotaBytesAttribute(): int
    {
        return (int) (($this->disk_quota_mb ?? 0) * 1024 * 1024);
    }

    /**
     * Get disk usage percentage.
     */
    public function getDiskUsagePercentAttribute(): float
    {
        if (! $this->disk_quota_mb || $this->disk_quota_mb <= 0) {
            return 0;
        }

        $used = $this->getDiskUsageBytes();
        $quota = $this->quota_bytes;

        return $quota > 0 ? min(100, round(($used / $quota) * 100, 1)) : 0;
    }
}
