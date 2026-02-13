<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

class DomainHotlinkSetting extends Model
{
    protected $fillable = [
        'domain_id',
        'is_enabled',
        'allowed_domains',
        'block_blank_referrer',
        'protected_extensions',
        'redirect_url',
    ];

    protected $casts = [
        'is_enabled' => 'boolean',
        'block_blank_referrer' => 'boolean',
    ];

    public function domain(): BelongsTo
    {
        return $this->belongsTo(Domain::class);
    }

    public function getAllowedDomainsArray(): array
    {
        if (empty($this->allowed_domains)) {
            return [];
        }
        // Split by newlines (form uses one domain per line)
        return array_filter(array_map('trim', preg_split('/[\r\n]+/', $this->allowed_domains)));
    }

    public function getProtectedExtensionsArray(): array
    {
        if (empty($this->protected_extensions)) {
            return [];
        }
        return array_filter(array_map('trim', explode(',', $this->protected_extensions)));
    }

    public static function getDefaultExtensions(): string
    {
        return 'jpg,jpeg,png,gif,webp,svg,mp4,mp3,pdf';
    }
}
