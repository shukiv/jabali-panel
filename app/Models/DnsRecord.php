<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

class DnsRecord extends Model
{
    protected $fillable = ['domain_id', 'name', 'type', 'content', 'ttl', 'priority'];

    protected function casts(): array
    {

        return ['ttl' => 'integer', 'priority' => 'integer'];

    }

    public function domain(): BelongsTo
    {
        return $this->belongsTo(Domain::class);
    }

    public function getFullNameAttribute(): string
    {
        return $this->name === '@' ? $this->domain->domain : $this->name.'.'.$this->domain->domain;
    }

    public static function getTypes(): array
    {
        return ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'NS', 'SRV', 'CAA'];
    }
}
