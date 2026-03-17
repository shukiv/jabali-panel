<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Models\Domain;
use App\Models\User;
use Illuminate\Console\Command;

class DomainCommand extends Command
{
    protected $signature = 'domain {action=list : list|create|show|delete} {--id= : Domain ID or name} {--user= : User ID or email} {--domain= : Domain name} {--force : Skip confirmation}';

    protected $description = 'Manage domains: list, create, show, delete';

    public function handle(): int
    {
        return match ($this->argument('action')) {
            'list' => $this->listDomains(),
            'create' => $this->createDomain(),
            'show' => $this->showDomain(),
            'delete' => $this->deleteDomain(),
            default => $this->error('Unknown action. Use: list, create, show, delete') ?? 1,
        };
    }

    private function listDomains(): int
    {
        $domains = Domain::with('user')->get();
        if ($domains->isEmpty()) {
            $this->warn('No domains found.');

            return 0;
        }
        $this->table(['ID', 'Domain', 'User', 'Document Root', 'SSL', 'Created'], $domains->map(fn ($d) => [$d->id, $d->domain, $d->user->email ?? '-', $d->document_root ?? '/var/www/'.$d->domain, $d->ssl_enabled ? '✓' : '✗', $d->created_at->format('Y-m-d')])->toArray());

        return 0;
    }

    private function createDomain(): int
    {
        $domain = $this->option('domain') ?? $this->ask('Domain name');

        // Clean and validate domain
        $domain = $this->cleanDomain($domain);

        // Validate FQDN format
        if (! $this->isValidFqdn($domain)) {
            $this->error("Invalid domain format: '$domain'");
            $this->line('Domain must be a valid FQDN (e.g., example.com, sub.example.com)');

            return 1;
        }

        $userId = $this->option('user') ?? $this->ask('User ID or email');
        $user = is_numeric($userId) ? User::find($userId) : User::where('email', $userId)->first();
        if (! $user) {
            $this->error("User not found: $userId");

            return 1;
        }
        if (Domain::where('domain', $domain)->exists()) {
            $this->error('Domain already exists!');

            return 1;
        }

        // Use proper document root structure
        $documentRoot = "/home/{$user->username}/domains/{$domain}/public_html";

        $d = Domain::create(['domain' => $domain, 'user_id' => $user->id, 'document_root' => $documentRoot]);
        $this->info("✓ Created domain #{$d->id}: {$domain}");
        $this->line("  Document root: {$documentRoot}");

        return 0;
    }

    private function cleanDomain(string $domain): string
    {
        // Remove protocol
        $domain = preg_replace('#^https?://#i', '', $domain);
        // Remove trailing slash and path
        $domain = strtok($domain, '/');
        // Remove port if present
        $domain = strtok($domain, ':');

        // Lowercase
        return strtolower(trim($domain));
    }

    private function isValidFqdn(string $domain): bool
    {
        // Check basic structure
        if (empty($domain) || strlen($domain) > 253) {
            return false;
        }
        if (strpos($domain, ' ') !== false) {
            return false;
        }
        if (! preg_match('/^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$/', $domain)) {
            return false;
        }

        // Must have at least one dot (e.g., example.com)
        if (strpos($domain, '.') === false) {
            return false;
        }

        // TLD must be at least 2 chars
        $parts = explode('.', $domain);
        $tld = end($parts);
        if (strlen($tld) < 2) {
            return false;
        }

        return true;
    }

    private function showDomain(): int
    {
        $domain = $this->findDomain();
        if (! $domain) {
            return 1;
        }
        $this->table(['Field', 'Value'], [['ID', $domain->id], ['Domain', $domain->domain], ['User', $domain->user->email ?? '-'], ['Document Root', $domain->document_root], ['SSL', $domain->ssl_enabled ? 'Yes' : 'No'], ['Created', $domain->created_at]]);

        return 0;
    }

    private function deleteDomain(): int
    {
        $domain = $this->findDomain();
        if (! $domain) {
            return 1;
        }
        if (! $this->option('force') && ! $this->confirm("Delete {$domain->domain}?")) {
            return 0;
        }
        $domain->delete();
        $this->info("✓ Deleted domain #{$domain->id}");

        return 0;
    }

    private function findDomain(): ?Domain
    {
        $id = $this->option('id') ?? $this->ask('Domain ID or name');
        $domain = is_numeric($id) ? Domain::find($id) : Domain::where('domain', $id)->first();
        if (! $domain) {
            $this->error("Domain not found: $id");
        }

        return $domain;
    }
}
