<?php

declare(strict_types=1);

namespace App\Models;

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
        'is_admin',
        'is_active',
        'hosting_package_id',
        'locale',
        'disk_quota_mb',
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

    public function isAdmin(): bool
    {
        return $this->is_admin;
    }

    public function getHomeDirectoryAttribute($value): string
    {
        return $value ?? "/home/{$this->username}";
    }

    protected static function booted()
    {
        static::deleting(function ($user) {
            if ($user->is_admin && (int) $user->getKey() === 1) {
                throw new \RuntimeException(__('Primary admin account cannot be deleted.'));
            }

            // Clean up email forwarders from system maps before DB cascade deletes them
            try {
                $agent = new \App\Services\Agent\AgentClient;
                $domains = $user->domains()->with('emailDomain.forwarders', 'emailDomain.domain')->get();

                foreach ($domains as $domain) {
                    $forwarders = $domain->emailDomain?->forwarders ?? collect();
                    foreach ($forwarders as $forwarder) {
                        $agent->send('email.forwarder_delete', [
                            'username' => $user->username,
                            'email' => $forwarder->email,
                        ]);
                    }
                }
            } catch (\Throwable $e) {
                \Log::warning("Failed to delete email forwarders for user {$user->username}: ".$e->getMessage());
            }

            // Delete master MySQL user when Jabali user is deleted
            $masterUser = $user->username.'_admin';

            try {
                if (class_exists(\mysqli::class)) {
                    // Use credentials from environment variables
                    $mysqli = new \mysqli(
                        config('database.connections.mysql.host', 'localhost'),
                        config('database.connections.mysql.username'),
                        config('database.connections.mysql.password')
                    );

                    if (! $mysqli->connect_error) {
                        // Use prepared statement to prevent SQL injection
                        // MySQL doesn't support prepared statements for DROP USER,
                        // so we validate the username format strictly
                        if (! preg_match('/^[a-zA-Z0-9_]+$/', $masterUser)) {
                            throw new \Exception('Invalid MySQL username format');
                        }

                        // Escape the username as an additional safety measure
                        $escapedUser = $mysqli->real_escape_string($masterUser);
                        $mysqli->query("DROP USER IF EXISTS '{$escapedUser}'@'localhost'");
                        $mysqli->close();
                    }
                }
            } catch (\Throwable $e) {
                \Log::error('Failed to delete master MySQL user: '.$e->getMessage());
            }

            try {
                \App\Models\MysqlCredential::where('user_id', $user->id)->delete();
            } catch (\Throwable $e) {
                \Log::error('Failed to delete stored MySQL credentials: '.$e->getMessage());
            }
        });
    }

    /**
     * Determine if the user can access the Filament panel.
     */
    public function canAccessPanel(\Filament\Panel $panel): bool
    {
        if ($panel->getId() === 'admin') {
            return $this->is_admin && $this->is_active;
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
     * Get disk usage in bytes.
     */
    public function getDiskUsageBytes(): int
    {
        // Disk usage must be obtained via the agent (root) to avoid permission-based undercounting.
        try {
            $agent = new \App\Services\Agent\AgentClient(
                (string) config('jabali.agent.socket', '/var/run/jabali/agent.sock'),
                (int) config('jabali.agent.timeout', 120),
            );

            $mount = $this->home_directory ?: ("/home/{$this->username}");
            $result = $agent->quotaGet($this->username, $mount);

            if (($result['success'] ?? false) && isset($result['used_mb'])) {
                return (int) ($result['used_mb'] * 1024 * 1024);
            }
        } catch (\Throwable $e) {
            \Log::warning('Disk usage read failed via agent: '.$e->getMessage(), [
                'username' => $this->username,
            ]);
        }

        return 0;
    }

    /**
     * Get formatted disk usage string.
     */
    public function getDiskUsageFormattedAttribute(): string
    {
        $bytes = $this->getDiskUsageBytes();

        return $this->formatBytes($bytes);
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

    /**
     * Format bytes to human readable string.
     */
    protected function formatBytes(int $bytes, int $precision = 1): string
    {
        $units = ['B', 'KB', 'MB', 'GB', 'TB'];
        $bytes = max($bytes, 0);
        $pow = floor(($bytes ? log($bytes) : 0) / log(1024));
        $pow = min($pow, count($units) - 1);
        $bytes /= pow(1024, $pow);

        return round($bytes, $precision).' '.$units[$pow];
    }
}
