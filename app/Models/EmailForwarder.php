<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

class EmailForwarder extends Model
{
    protected $fillable = [
        'email_domain_id',
        'user_id',
        'local_part',
        'destinations',
        'is_active',
    ];

    protected $casts = [
        'destinations' => 'array',
        'is_active' => 'boolean',
    ];

    public function emailDomain(): BelongsTo
    {
        return $this->belongsTo(EmailDomain::class);
    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function getEmailAttribute(): string
    {
        return $this->local_part . '@' . $this->emailDomain->domain->domain;
    }

    public function getDestinationsFormattedAttribute(): string
    {
        return implode(', ', $this->destinations ?? []);
    }
}
