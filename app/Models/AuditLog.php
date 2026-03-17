<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Support\Facades\Auth;

class AuditLog extends Model
{
    protected $fillable = [
        'user_id',
        'action',
        'category',
        'description',
        'target_type',
        'target_id',
        'target_name',
        'ip_address',
        'user_agent',
        'metadata',
    ];

    protected function casts(): array
    {
        return [
            'metadata' => 'array',
        ];
    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    /**
     * Log a privileged action.
     */
    public static function log(
        string $action,
        string $category,
        string $description,
        ?string $targetType = null,
        ?int $targetId = null,
        ?string $targetName = null,
        ?array $metadata = null
    ): self {
        return self::create([
            'user_id' => Auth::id(),
            'action' => $action,
            'category' => $category,
            'description' => $description,
            'target_type' => $targetType,
            'target_id' => $targetId,
            'target_name' => $targetName,
            'ip_address' => request()->ip(),
            'user_agent' => request()->userAgent(),
            'metadata' => $metadata,
        ]);
    }

    /**
     * Log user management actions.
     */
    public static function logUserAction(string $action, User $targetUser, ?array $metadata = null): self
    {
        return self::log(
            action: $action,
            category: 'user',
            description: "{$action} user: {$targetUser->username}",
            targetType: 'user',
            targetId: $targetUser->id,
            targetName: $targetUser->username,
            metadata: $metadata
        );
    }

    /**
     * Log domain management actions.
     */
    public static function logDomainAction(string $action, string $domain, ?int $domainId = null, ?array $metadata = null): self
    {
        return self::log(
            action: $action,
            category: 'domain',
            description: "{$action} domain: {$domain}",
            targetType: 'domain',
            targetId: $domainId,
            targetName: $domain,
            metadata: $metadata
        );
    }

    /**
     * Log database management actions.
     */
    public static function logDatabaseAction(string $action, string $database, ?array $metadata = null): self
    {
        return self::log(
            action: $action,
            category: 'database',
            description: "{$action} database: {$database}",
            targetType: 'database',
            targetId: null,
            targetName: $database,
            metadata: $metadata
        );
    }

    /**
     * Log service management actions.
     */
    public static function logServiceAction(string $action, string $service, ?array $metadata = null): self
    {
        return self::log(
            action: $action,
            category: 'service',
            description: "{$action} service: {$service}",
            targetType: 'service',
            targetId: null,
            targetName: $service,
            metadata: $metadata
        );
    }

    /**
     * Log email/mailbox management actions.
     */
    public static function logEmailAction(string $action, string $email, ?array $metadata = null): self
    {
        return self::log(
            action: $action,
            category: 'email',
            description: "{$action} mailbox: {$email}",
            targetType: 'mailbox',
            targetId: null,
            targetName: $email,
            metadata: $metadata
        );
    }

    /**
     * Log firewall actions.
     */
    public static function logFirewallAction(string $action, ?string $rule = null, ?array $metadata = null): self
    {
        return self::log(
            action: $action,
            category: 'firewall',
            description: $rule ? "{$action} firewall rule: {$rule}" : "{$action} firewall",
            targetType: 'firewall',
            targetId: null,
            targetName: $rule,
            metadata: $metadata
        );
    }

    /**
     * Log authentication events.
     */
    public static function logAuth(string $action, ?User $user = null, ?array $metadata = null): self
    {
        $username = $user?->username ?? Auth::user()?->username ?? 'unknown';

        return self::create([
            'user_id' => $user?->id ?? Auth::id(),
            'action' => $action,
            'category' => 'auth',
            'description' => "{$action}: {$username}",
            'target_type' => 'user',
            'target_id' => $user?->id ?? Auth::id(),
            'target_name' => $username,
            'ip_address' => request()->ip(),
            'user_agent' => request()->userAgent(),
            'metadata' => $metadata,
        ]);
    }

    /**
     * Prune audit logs older than the specified number of days.
     *
     * @param  int|null  $days  Number of days to keep (default: from settings or 90)
     * @return int Number of deleted records
     */
    public static function prune(?int $days = null): int
    {
        $days = $days ?? (int) DnsSetting::get('audit_log_retention_days', 90);

        return self::where('created_at', '<', now()->subDays($days))->delete();
    }
}
