<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Support\Facades\Crypt;

class CloudflareZone extends Model
{
    protected $fillable = [
        'user_id',
        'domain_id',
        'zone_id',
        'account_id',
        'api_token',
    ];

    protected $hidden = [
        'api_token',
    ];

    public function setApiTokenAttribute(string $value): void
    {
        $this->attributes['api_token'] = Crypt::encryptString($value);
    }

    public function getApiTokenAttribute(): string
    {
        $value = $this->attributes['api_token'] ?? '';
        if ($value === '') {
            return '';
        }

        return Crypt::decryptString($value);
    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function domain(): BelongsTo
    {
        return $this->belongsTo(Domain::class);
    }
}
