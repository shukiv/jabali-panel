<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Support\Str;

class ImpersonationToken extends Model
{
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

    public static function createForUser(User $admin, User $targetUser, ?string $ipAddress = null): self
    {
        // Clean up old tokens for this admin/user combination
        static::where('admin_id', $admin->id)
            ->where('target_user_id', $targetUser->id)
            ->delete();

        return static::create([
            'admin_id' => $admin->id,
            'target_user_id' => $targetUser->id,
            'token' => Str::random(64),
            'expires_at' => now()->addMinutes(5),
            'ip_address' => $ipAddress,
        ]);
    }

    public function isValid(): bool
    {
        return $this->expires_at->isFuture() && $this->used_at === null;
    }

    public function markAsUsed(): void
    {
        $this->update(['used_at' => now()]);
    }

    public static function findValidToken(string $token, ?string $ipAddress = null): ?self
    {
        $record = static::where('token', $token)
            ->where('expires_at', '>', now())
            ->whereNull('used_at')
            ->first();

        if (! $record) {
            return null;
        }

        if ($ipAddress && $record->ip_address && $record->ip_address !== $ipAddress) {
            return null;
        }

        return $record;
    }

    public static function cleanupExpired(): int
    {
        return static::where('expires_at', '<', now())
            ->orWhereNotNull('used_at')
            ->delete();
    }
}
