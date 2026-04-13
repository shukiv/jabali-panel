<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

class SslCertificate extends Model
{
    /** @use HasFactory<\Database\Factories\SslCertificateFactory> */
    use HasFactory;

    protected $fillable = [
        'domain_id',
        'service',
        'type',
        'status',
        'issuer',
        'certificate',
        'private_key',
        'ca_bundle',
        'issued_at',
        'expires_at',
        'last_check_at',
        'last_error',
        'renewal_attempts',
        'auto_renew',
    ];

    protected function casts(): array
    {

        return [
            'issued_at' => 'datetime',
            'expires_at' => 'datetime',
            'last_check_at' => 'datetime',
            'auto_renew' => 'boolean',
            'private_key' => 'encrypted',
        ];

    }

    protected $hidden = [
        'private_key',
    ];

    public function domain(): BelongsTo
    {
        return $this->belongsTo(Domain::class);
    }

    public function isActive(): bool
    {
        return $this->status === 'active' && $this->expires_at && $this->expires_at->isFuture();
    }

    public function isExpired(): bool
    {
        return $this->expires_at && $this->expires_at->isPast();
    }

    public function isExpiringSoon(int $days = 30): bool
    {
        if (! $this->expires_at) {
            return false;
        }
        $daysUntilExpiry = now()->diffInDays($this->expires_at, false);

        return $daysUntilExpiry >= 0 && $daysUntilExpiry <= $days;
    }

    public function getDaysUntilExpiryAttribute(): ?int
    {
        if (! $this->expires_at) {
            return null;
        }

        return (int) now()->diffInDays($this->expires_at, false);
    }

    public function getStatusColorAttribute(): string
    {
        if ($this->isExpired()) {
            return 'danger';
        }

        if ($this->isExpiringSoon(7)) {
            return 'danger';
        }

        if ($this->isExpiringSoon(30)) {
            return 'warning';
        }

        return match ($this->status) {
            'active' => 'success',
            'pending' => 'info',
            'failed' => 'danger',
            'revoked' => 'danger',
            'expired' => 'danger',
            default => 'gray',
        };
    }

    public function getStatusLabelAttribute(): string
    {
        if ($this->isExpired()) {
            return 'Expired';
        }

        if ($this->status === 'active' && $this->isExpiringSoon(7)) {
            return 'Expiring Soon';
        }

        return match ($this->status) {
            'active' => 'Active',
            'pending' => 'Pending',
            'failed' => 'Failed',
            'revoked' => 'Revoked',
            'expired' => 'Expired',
            default => 'Unknown',
        };
    }

    public function getTypeLabelAttribute(): string
    {
        return match ($this->type) {
            'none' => 'No SSL',
            'self_signed' => 'Self-Signed',
            'lets_encrypt' => "Let's Encrypt",
            'custom' => 'Custom',
            default => 'Unknown',
        };
    }

    public function needsRenewal(): bool
    {
        return $this->auto_renew
            && $this->type === 'lets_encrypt'
            && $this->status === 'active'
            && $this->isExpiringSoon(30);
    }
}
