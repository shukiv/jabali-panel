<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Support\Str;

class ImpersonationToken extends Model
{
    /**
     * The unhashed token, only available immediately after createForUser().
     * Not persisted to the database.
     */
    public ?string $raw_token = null;

    protected $fillable = [
        'admin_id',
        'target_user_id',
        'token',
        'expires_at',
        'used_at',
        'ip_address',
    ];

    protected function casts(): array
    {

        return [
            'expires_at' => 'datetime',
            'used_at' => 'datetime',
        ];

    }

    public function admin(): BelongsTo
    {
        return $this->belongsTo(User::class, 'admin_id');
    }

    public function targetUser(): BelongsTo
    {
        return $this->belongsTo(User::class, 'target_user_id');
    }

    /**
     * Create a token for impersonation. Returns the model with a `raw_token`
     * attribute containing the unhashed token for use in URLs. The database
     * stores only the SHA-256 hash.
     */
    public static function createForUser(User $admin, User $targetUser, ?string $ipAddress = null): self
    {
        // Clean up old tokens for this admin/user combination
        static::where('admin_id', $admin->id)
            ->where('target_user_id', $targetUser->id)
            ->delete();

        $rawToken = Str::random(64);

        $record = static::create([
            'admin_id' => $admin->id,
            'target_user_id' => $targetUser->id,
            'token' => hash('sha256', $rawToken),
            'expires_at' => now()->addMinutes(5),
            'ip_address' => $ipAddress,
        ]);

        // Attach raw token so the caller can build the URL
        $record->raw_token = $rawToken;

        return $record;
    }

    public function markAsUsed(): void
    {
        $this->update(['used_at' => now()]);
    }

    public static function findValidToken(string $token, ?string $ipAddress = null): ?self
    {
        $hashedToken = hash('sha256', $token);

        $record = static::where('token', $hashedToken)
            ->where('expires_at', '>', now())
            ->whereNull('used_at')
            ->first();

        if (! $record) {
            return null;
        }

        // Always validate IP — reject if either IP is missing or mismatched
        if (! $ipAddress || ! $record->ip_address || $record->ip_address !== $ipAddress) {
            return null;
        }

        return $record;
    }

    /**
     * Prune spent and expired impersonation tokens.
     *
     * @api Intended for the scheduled tokens:prune maintenance task;
     *      createForUser() already garbage-collects stale tokens for the
     *      current admin/target pair, so day-to-day operation doesn't need
     *      this. Kept for ops to invoke manually or on a scheduled hook.
     */
    public static function cleanupExpired(): int
    {
        return static::where('expires_at', '<', now())
            ->orWhereNotNull('used_at')
            ->delete();
    }
}
