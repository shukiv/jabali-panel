<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;
use Illuminate\Database\Eloquent\Relations\HasOne;

class Domain extends Model
{
    use HasFactory;

    protected static function booted(): void
    {
        // Clean up related records when a domain is deleted
        static::deleting(function (Domain $domain) {
            // Clean up email forwarders from system maps before DB cascade deletes them
            try {
                $agent = app(\App\Services\Agent\AgentClient::class);
                $domain->loadMissing('user', 'emailDomain.forwarders');
                $username = $domain->user?->username;

                if ($username && $domain->emailDomain) {
                    foreach ($domain->emailDomain->forwarders as $forwarder) {
                        $agent->send('email.forwarder_delete', [
                            'username' => $username,
                            'email' => $forwarder->email,
                        ]);
                    }
                }
            } catch (\Exception $e) {
                \Log::warning("Failed to delete email forwarders for domain {$domain->domain}: ".$e->getMessage());
            }

            // Delete SSL certificates (web and mail)
            $domain->sslCertificates()->delete();

            // Delete DNS records
            $domain->dnsRecords()->delete();

            // Delete redirects
            $domain->redirects()->delete();

            // Delete hotlink settings
            $domain->hotlinkSetting?->delete();

            // Delete aliases
            $domain->aliases()->delete();

            // Delete email domain and related records
            if ($domain->emailDomain) {
                // Delete mailboxes (which will cascade to autoresponders)
                $domain->emailDomain->mailboxes()->delete();
                // Delete forwarders
                $domain->emailDomain->forwarders()->delete();
                // Delete email domain
                $domain->emailDomain->delete();
            }
        });
    }

    protected $fillable = [
        'user_id',
        'domain',
        'document_root',
        'ip_address',
        'ipv6_address',
        'is_active',
        'ssl_enabled',
        'directory_index',
        'page_cache_enabled',
    ];

    protected $casts = [
        'is_active' => 'boolean',
        'ssl_enabled' => 'boolean',
        'page_cache_enabled' => 'boolean',
    ];

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function dnsRecords(): HasMany
    {
        return $this->hasMany(DnsRecord::class);
    }

    public function emailDomain(): HasOne
    {
        return $this->hasOne(EmailDomain::class);
    }

    public function sslCertificate(): HasOne
    {
        return $this->hasOne(SslCertificate::class)->where('service', 'web');
    }

    public function mailSslCertificate(): HasOne
    {
        return $this->hasOne(SslCertificate::class)->where('service', 'mail');
    }

    public function sslCertificates(): HasMany
    {
        return $this->hasMany(SslCertificate::class);
    }

    public function redirects(): HasMany
    {
        return $this->hasMany(DomainRedirect::class);
    }

    public function aliases(): HasMany
    {
        return $this->hasMany(DomainAlias::class);
    }

    public function hotlinkSetting(): HasOne
    {
        return $this->hasOne(DomainHotlinkSetting::class);
    }

    public function getOrCreateHotlinkSetting(): DomainHotlinkSetting
    {
        return $this->hotlinkSetting ?? $this->hotlinkSetting()->create([
            'is_enabled' => false,
            'protected_extensions' => DomainHotlinkSetting::getDefaultExtensions(),
        ]);
    }

    /**
     * Check if email is enabled for this domain
     */
    public function hasEmailEnabled(): bool
    {
        return $this->emailDomain()->exists() && $this->emailDomain->is_active;
    }

    /**
     * Check if SSL is active for this domain
     */
    public function hasSslActive(): bool
    {
        return $this->sslCertificate()->exists() && $this->sslCertificate->isActive();
    }

    /**
     * Get SSL status for display
     */
    public function getSslStatusAttribute(): string
    {
        if (! $this->sslCertificate) {
            return 'No SSL';
        }

        return $this->sslCertificate->status_label;
    }
}
