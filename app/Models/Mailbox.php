<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasOne;
use Illuminate\Support\Facades\Crypt;

class Mailbox extends Model
{
    protected $fillable = [
        'email_domain_id',
        'user_id',
        'local_part',
        'password_hash',
        'password_encrypted',
        'maildir_path',
        'system_uid',
        'system_gid',
        'name',
        'quota_bytes',
        'quota_used_bytes',
        'is_active',
        'imap_enabled',
        'pop3_enabled',
        'smtp_enabled',
        'last_login_at',
    ];

    protected $casts = [
        'is_active' => 'boolean',
        'imap_enabled' => 'boolean',
        'pop3_enabled' => 'boolean',
        'smtp_enabled' => 'boolean',
        'quota_bytes' => 'integer',
        'quota_used_bytes' => 'integer',
        'last_login_at' => 'datetime',
    ];

    protected $hidden = [
        'password_hash',
        'password_encrypted',
    ];

    /**
     * Get decrypted password for SSO
     */
    public function getPlainPasswordAttribute(): ?string
    {
        if (empty($this->password_encrypted)) {
            return null;
        }
        try {
            return Crypt::decryptString($this->password_encrypted);
        } catch (\Exception $e) {
            return null;
        }
    }

    /**
     * Store encrypted password
     */
    public function setPlainPassword(string $password): void
    {
        $this->password_encrypted = Crypt::encryptString($password);
        $this->save();
    }

    public function emailDomain(): BelongsTo
    {
        return $this->belongsTo(EmailDomain::class);
    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function autoresponder(): HasOne
    {
        return $this->hasOne(Autoresponder::class);
    }

    /**
     * Get the full email address (e.g., user@example.com)
     */
    public function getEmailAttribute(): string
    {
        return $this->local_part . '@' . $this->emailDomain->domain_name;
    }

    /**
     * Get quota usage as a percentage
     */
    public function getQuotaPercentAttribute(): float
    {
        if ($this->quota_bytes <= 0) {
            return 0;
        }

        return round(($this->quota_used_bytes / $this->quota_bytes) * 100, 1);
    }

    /**
     * Get the Maildir path for this mailbox
     */
    public function getMaildirPathAttribute(): string
    {
        return $this->emailDomain->domain_name . '/' . $this->local_part . '/';
    }

    /**
     * Format quota for display (e.g., "500 MB", "1 GB")
     */
    public function getQuotaFormattedAttribute(): string
    {
        return $this->formatBytes($this->quota_bytes);
    }

    /**
     * Format quota used for display
     */
    public function getQuotaUsedFormattedAttribute(): string
    {
        return $this->formatBytes($this->quota_used_bytes);
    }

    /**
     * Check if mailbox is near quota (>80%)
     */
    public function isNearQuota(): bool
    {
        return $this->quota_percent >= 80;
    }

    /**
     * Check if mailbox is over quota
     */
    public function isOverQuota(): bool
    {
        return $this->quota_used_bytes >= $this->quota_bytes;
    }

    /**
     * Format bytes to human readable
     */
    protected function formatBytes(int $bytes): string
    {
        if ($bytes < 1024) {
            return $bytes . ' B';
        }
        if ($bytes < 1048576) {
            return round($bytes / 1024, 1) . ' KB';
        }
        if ($bytes < 1073741824) {
            return round($bytes / 1048576, 1) . ' MB';
        }

        return round($bytes / 1073741824, 1) . ' GB';
    }
}
