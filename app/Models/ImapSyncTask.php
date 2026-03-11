<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Builder;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Support\Facades\Crypt;

class ImapSyncTask extends Model
{
    protected $fillable = [
        'user_id',
        'mailbox_id',
        'source_host',
        'source_port',
        'source_encryption',
        'source_email',
        'source_password_encrypted',
        'folders',
        'batch_id',
        'status',
        'messages_synced',
        'messages_total',
        'error_message',
        'log_path',
        'started_at',
        'completed_at',
    ];

    protected $hidden = [
        'source_password_encrypted',
    ];

    protected function casts(): array
    {
        return [
            'folders' => 'array',
            'started_at' => 'datetime',
            'completed_at' => 'datetime',
            'messages_synced' => 'integer',
            'messages_total' => 'integer',
        ];
    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function mailbox(): BelongsTo
    {
        return $this->belongsTo(Mailbox::class);
    }

    public function getSourcePasswordAttribute(): ?string
    {
        if (empty($this->source_password_encrypted)) {
            return null;
        }

        return Crypt::decryptString($this->source_password_encrypted);
    }

    public function setSourcePasswordAttribute(string $value): void
    {
        $this->attributes['source_password_encrypted'] = Crypt::encryptString($value);
    }

    public function scopeForUser(Builder $query, int $userId): Builder
    {
        return $query->where('user_id', $userId);
    }

    public function scopeActive(Builder $query): Builder
    {
        return $query->whereIn('status', ['pending', 'running']);
    }
}
