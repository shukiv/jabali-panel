<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Models\DnsRecord;
use App\Models\DnsSetting;
use App\Models\Domain;
use App\Services\Agent\AgentClient;
use App\Support\ServerFacts;
use Exception;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\Log;

class MailAutoconfigSyncCommand extends Command
{
    protected $signature = 'jabali:mail-autoconfig-sync';

    protected $description = 'Sync autoconfig/autodiscover DNS records for all email-enabled domains';

    public function handle(): int
    {
        $this->info('Syncing mail autoconfig DNS records...');

        $domains = Domain::whereHas('emailDomain')->get();
        $totalAdded = 0;
        $modifiedDomains = [];
        $serverIp = ServerFacts::serverIp('127.0.0.1');
        $defaultTtl = 3600;

        foreach ($domains as $domain) {
            $added = 0;

            $records = [
                ['name' => 'autoconfig', 'type' => 'A', 'content' => $serverIp, 'ttl' => $defaultTtl],
                ['name' => 'autodiscover', 'type' => 'A', 'content' => $serverIp, 'ttl' => $defaultTtl],
                ['name' => '_imaps._tcp', 'type' => 'SRV', 'content' => "1 993 mail.{$domain->domain}.", 'ttl' => $defaultTtl, 'priority' => 0],
                ['name' => '_pop3s._tcp', 'type' => 'SRV', 'content' => "1 995 mail.{$domain->domain}.", 'ttl' => $defaultTtl, 'priority' => 0],
                ['name' => '_submission._tcp', 'type' => 'SRV', 'content' => "1 587 mail.{$domain->domain}.", 'ttl' => $defaultTtl, 'priority' => 0],
            ];

            foreach ($records as $record) {
                $exists = DnsRecord::where('domain_id', $domain->id)
                    ->where('name', $record['name'])
                    ->where('type', $record['type'])
                    ->exists();

                if (! $exists) {
                    DnsRecord::create(array_merge(['domain_id' => $domain->id], $record));
                    $added++;
                }
            }

            if ($added > 0) {
                $totalAdded += $added;
                $modifiedDomains[] = $domain;
                $this->line("  Added {$added} records for {$domain->domain}");
            }
        }

        // Sync DNS zones for modified domains
        foreach ($modifiedDomains as $domain) {
            try {
                $allRecords = DnsRecord::where('domain_id', $domain->id)->get()->toArray();
                $settings = DnsSetting::getAll();
                $hostname = gethostname() ?: 'localhost';
                $serverIpv6 = $settings['default_ipv6'] ?? null;

                $agent = app(AgentClient::class);
                $agent->send('dns.sync_zone', [
                    'domain' => $domain->domain,
                    'records' => $allRecords,
                    'ns1' => $settings['ns1'] ?? "ns1.{$hostname}",
                    'ns2' => $settings['ns2'] ?? "ns2.{$hostname}",
                    'admin_email' => $settings['admin_email'] ?? "admin.{$hostname}",
                    'default_ip' => $settings['default_ip'] ?? $serverIp,
                    'default_ipv6' => $serverIpv6,
                    'default_ttl' => $settings['default_ttl'] ?? 3600,
                ]);
            } catch (Exception $e) {
                $this->warn("  Failed to sync DNS zone for {$domain->domain}: {$e->getMessage()}");
                Log::warning("Failed to sync DNS zone for {$domain->domain}: {$e->getMessage()}");
            }
        }

        $this->newLine();
        $this->info("Done. Added {$totalAdded} records across {$domains->count()} domains.");

        return 0;
    }
}
