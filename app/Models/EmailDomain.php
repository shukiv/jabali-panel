<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

class EmailDomain extends Model
{
    protected $fillable = [
        'domain_id',
        'is_active',
        'dkim_selector',
        'dkim_private_key',
        'dkim_public_key',
        'catch_all_enabled',
        'catch_all_address',
        'max_mailboxes',
        'max_quota_bytes',
    ];

    protected $casts = [
        'is_active' => 'boolean',
        'catch_all_enabled' => 'boolean',
        'max_mailboxes' => 'integer',
        'max_quota_bytes' => 'integer',
    ];

    protected $hidden = [
        'dkim_private_key',
    ];

    public function domain(): BelongsTo
    {
        return $this->belongsTo(Domain::class);
    }

    public function mailboxes(): HasMany
    {
        return $this->hasMany(Mailbox::class);
    }

    /**
     * Get the domain name (e.g., example.com)
     */
    public function getDomainNameAttribute(): string
    {
        return $this->domain->domain;
    }

    /**
     * Get total quota used across all mailboxes
     */
    public function getTotalQuotaUsedAttribute(): int
    {
        return (int) $this->mailboxes()->sum('quota_used_bytes');
    }

    /**
     * Get the number of mailboxes
     */
    public function getMailboxCountAttribute(): int
    {
        return $this->mailboxes()->count();
    }

    /**
     * Check if more mailboxes can be created
     */
    public function canCreateMailbox(): bool
    {
        return $this->mailbox_count < $this->max_mailboxes;
    }
}
