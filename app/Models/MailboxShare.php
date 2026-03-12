<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

class MailboxShare extends Model
{
    protected $fillable = [
        'mailbox_id',
        'shared_with_mailbox_id',
        'folder',
        'acl_rights',
    ];

    public function mailbox(): BelongsTo
    {
        return $this->belongsTo(Mailbox::class);
    }

    public function sharedWith(): BelongsTo
    {
        return $this->belongsTo(Mailbox::class, 'shared_with_mailbox_id');
    }

    public function getPermissionLevelAttribute(): string
    {
        return match ($this->acl_rights) {
            'lrs' => 'Read',
            'lrswite' => 'Read & Write',
            'lrswitekx' => 'Full Access',
            'lrswitekxa' => 'Admin',
            default => 'Custom',
        };
    }
}
