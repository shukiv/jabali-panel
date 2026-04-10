<?php

declare(strict_types=1);

namespace Database\Seeders;

use App\Models\AuditLog;
use App\Models\CronJob;
use App\Models\DnsRecord;
use App\Models\Domain;
use App\Models\EmailDomain;
use App\Models\EmailForwarder;
use App\Models\HostingPackage;
use App\Models\Mailbox;
use App\Models\MysqlCredential;
use App\Models\SslCertificate;
use App\Models\User;
use Illuminate\Database\Seeder;
use Illuminate\Support\Facades\Crypt;
use Illuminate\Support\Facades\Hash;

/**
 * Seeds demo data for DEMO_MODE.
 *
 * Run with: php artisan db:seed --class=DemoSeeder
 */
class DemoSeeder extends Seeder
{
    public function run(): void
    {
        $this->seedPackages();
        $this->seedAdmin();
        $users = $this->seedUsers();
        $this->seedDomains($users);
        $this->seedEmails($users);
        $this->seedDatabases($users);
        $this->seedCronJobs($users);
        $this->seedAuditLogs($users);
    }

    private function seedPackages(): void
    {
        HostingPackage::firstOrCreate(
            ['name' => 'Starter'],
            [
                'description' => 'Basic shared hosting for small websites',
                'disk_quota_mb' => 1024,
                'bandwidth_gb' => 50,
                'domains_limit' => 3,
                'databases_limit' => 3,
                'mailboxes_limit' => 10,
                'cpu_quota' => 100,
                'memory_limit_mb' => 512,
                'max_processes' => 50,
                'nginx_req_per_sec' => 50,
                'nginx_connections' => 100,
                'ssh_isolation_mode' => 'sandbox',
                'is_active' => true,
            ]
        );

        HostingPackage::firstOrCreate(
            ['name' => 'Professional'],
            [
                'description' => 'High performance hosting for businesses',
                'disk_quota_mb' => 10240,
                'bandwidth_gb' => 500,
                'domains_limit' => 25,
                'databases_limit' => 25,
                'mailboxes_limit' => 100,
                'cpu_quota' => 200,
                'memory_limit_mb' => 2048,
                'max_processes' => 200,
                'nginx_req_per_sec' => 100,
                'nginx_connections' => 200,
                'ssh_isolation_mode' => 'container',
                'is_active' => true,
            ]
        );

        HostingPackage::firstOrCreate(
            ['name' => 'Enterprise'],
            [
                'description' => 'Dedicated resources for high-traffic sites',
                'disk_quota_mb' => 51200,
                'bandwidth_gb' => 2000,
                'domains_limit' => null,
                'databases_limit' => null,
                'mailboxes_limit' => null,
                'cpu_quota' => 400,
                'memory_limit_mb' => 4096,
                'max_processes' => 500,
                'nginx_req_per_sec' => null,
                'nginx_connections' => null,
                'ssh_isolation_mode' => 'container',
                'is_active' => true,
            ]
        );
    }

    private function seedAdmin(): void
    {
        $admin = User::firstOrCreate(
            ['email' => 'admin@demo.jabali-panel.com'],
            [
                'name' => 'Demo Admin',
                'username' => 'admin',
                'password' => Hash::make('demo'),
                'is_active' => true,
            ]
        );
        $admin->is_admin = true;
        $admin->password = Hash::make('demo');
        $admin->save();
    }

    /** @return array<string, User> */
    private function seedUsers(): array
    {
        $starter = HostingPackage::where('name', 'Starter')->first();
        $pro = HostingPackage::where('name', 'Professional')->first();
        $enterprise = HostingPackage::where('name', 'Enterprise')->first();

        $userData = [
            'sarah' => ['name' => 'Sarah Chen', 'email' => 'sarah@demo.jabali-panel.com', 'package' => $starter],
            'david' => ['name' => 'David Miller', 'email' => 'david@demo.jabali-panel.com', 'package' => $pro],
            'emma' => ['name' => 'Emma Wilson', 'email' => 'emma@demo.jabali-panel.com', 'package' => $starter],
            'alex' => ['name' => 'Alex Rodriguez', 'email' => 'alex@demo.jabali-panel.com', 'package' => $enterprise],
            'noor' => ['name' => 'Noor Hassan', 'email' => 'noor@demo.jabali-panel.com', 'package' => $pro],
        ];

        $users = [];
        foreach ($userData as $username => $data) {
            $users[$username] = User::firstOrCreate(
                ['email' => $data['email']],
                [
                    'name' => $data['name'],
                    'username' => $username,
                    'password' => Hash::make('demo'),
                    'hosting_package_id' => $data['package']->id,
                    'disk_quota_mb' => $data['package']->disk_quota_mb,
                    'is_active' => true,
                ]
            );
        }

        return $users;
    }

    /** @param array<string, User> $users */
    private function seedDomains(array $users): void
    {
        $domainData = [
            'sarah' => ['freshbakery.co', 'sarahchen.dev'],
            'david' => ['millertech.io', 'davidmiller.com', 'api.millertech.io'],
            'emma' => ['greenleaf-shop.com'],
            'alex' => ['enterprise-app.com', 'staging.enterprise-app.com', 'docs.enterprise-app.com', 'shop.enterprise-app.com'],
            'noor' => ['noordesign.studio', 'portfolio.noordesign.studio'],
        ];

        foreach ($domainData as $username => $domains) {
            $user = $users[$username] ?? null;
            if (! $user) {
                continue;
            }

            foreach ($domains as $domainName) {
                $domain = Domain::firstOrCreate(
                    ['domain' => $domainName],
                    [
                        'user_id' => $user->id,
                        'is_active' => true,
                    ]
                );

                // Add SSL certificate
                SslCertificate::firstOrCreate(
                    ['domain_id' => $domain->id, 'service' => 'web'],
                    [
                        'type' => 'letsencrypt',
                        'status' => 'active',
                        'issuer' => "Let's Encrypt",
                        'issued_at' => now()->subDays(rand(5, 60)),
                        'expires_at' => now()->addDays(rand(30, 85)),
                    ]
                );

                // Add DNS records
                DnsRecord::firstOrCreate(
                    ['domain_id' => $domain->id, 'name' => $domainName, 'type' => 'A'],
                    ['content' => '203.0.113.'.rand(10, 250), 'ttl' => 3600]
                );
                DnsRecord::firstOrCreate(
                    ['domain_id' => $domain->id, 'name' => 'www.'.$domainName, 'type' => 'CNAME'],
                    ['content' => $domainName, 'ttl' => 3600]
                );
                DnsRecord::firstOrCreate(
                    ['domain_id' => $domain->id, 'name' => $domainName, 'type' => 'MX'],
                    ['content' => 'mail.'.$domainName, 'ttl' => 3600, 'priority' => 10]
                );
            }
        }
    }

    /** @param array<string, User> $users */
    private function seedEmails(array $users): void
    {
        $emailData = [
            'sarah' => [
                'domain' => 'freshbakery.co',
                'mailboxes' => ['info', 'orders', 'sarah'],
                'forwarders' => ['support' => 'sarah@freshbakery.co'],
            ],
            'david' => [
                'domain' => 'millertech.io',
                'mailboxes' => ['david', 'support', 'billing', 'noreply'],
                'forwarders' => ['admin' => 'david@millertech.io', 'sales' => 'david@millertech.io'],
            ],
            'emma' => [
                'domain' => 'greenleaf-shop.com',
                'mailboxes' => ['emma', 'shop', 'returns'],
                'forwarders' => [],
            ],
            'alex' => [
                'domain' => 'enterprise-app.com',
                'mailboxes' => ['alex', 'devops', 'alerts', 'support', 'hr', 'finance'],
                'forwarders' => ['ceo' => 'alex@enterprise-app.com', 'team' => 'alex@enterprise-app.com'],
            ],
            'noor' => [
                'domain' => 'noordesign.studio',
                'mailboxes' => ['noor', 'hello', 'projects'],
                'forwarders' => ['contact' => 'noor@noordesign.studio'],
            ],
        ];

        foreach ($emailData as $username => $data) {
            $user = $users[$username] ?? null;
            $domain = Domain::where('domain', $data['domain'])->first();
            if (! $user || ! $domain) {
                continue;
            }

            $emailDomain = EmailDomain::firstOrCreate(
                ['domain_id' => $domain->id],
                ['is_active' => true]
            );

            foreach ($data['mailboxes'] as $local) {
                Mailbox::firstOrCreate(
                    ['email_domain_id' => $emailDomain->id, 'local_part' => $local],
                    [
                        'user_id' => $user->id,
                        'name' => ucfirst($local),
                        'password_hash' => Hash::make('demo'),
                        'quota_bytes' => 1073741824, // 1GB
                        'quota_used_bytes' => rand(1048576, 536870912),
                        'is_active' => true,
                        'imap_enabled' => true,
                        'smtp_enabled' => true,
                    ]
                );
            }

            foreach ($data['forwarders'] as $from => $to) {
                EmailForwarder::firstOrCreate(
                    ['email_domain_id' => $emailDomain->id, 'source' => $from],
                    [
                        'destination' => $to,
                        'is_active' => true,
                    ]
                );
            }
        }
    }

    /** @param array<string, User> $users */
    private function seedDatabases(array $users): void
    {
        $dbData = [
            'sarah' => ['sarah_bakery', 'sarah_wp_main'],
            'david' => ['david_millertech', 'david_wp_blog', 'david_api_prod', 'david_analytics'],
            'emma' => ['emma_greenleaf', 'emma_wp_shop'],
            'alex' => ['alex_enterprise', 'alex_staging', 'alex_analytics', 'alex_logs', 'alex_wp_docs'],
            'noor' => ['noor_portfolio', 'noor_wp_studio'],
        ];

        foreach ($dbData as $username => $databases) {
            $user = $users[$username] ?? null;
            if (! $user) {
                continue;
            }

            foreach ($databases as $dbName) {
                MysqlCredential::firstOrCreate(
                    ['user_id' => $user->id, 'mysql_username' => $dbName],
                    ['mysql_password_encrypted' => Crypt::encryptString('demo_pass_'.rand(1000, 9999))]
                );
            }
        }
    }

    /** @param array<string, User> $users */
    private function seedCronJobs(array $users): void
    {
        $cronData = [
            'sarah' => [
                ['name' => 'WordPress Cron', 'schedule' => '*/15 * * * *', 'command' => 'wp cron event run --due-now --path=/home/sarah/domains/freshbakery.co/public_html'],
            ],
            'david' => [
                ['name' => 'WordPress Cron', 'schedule' => '*/5 * * * *', 'command' => 'wp cron event run --due-now --path=/home/david/domains/millertech.io/public_html'],
                ['name' => 'DB Backup', 'schedule' => '0 3 * * *', 'command' => 'mysqldump david_millertech | gzip > /home/david/backups/db-$(date +\\%Y\\%m\\%d).sql.gz'],
                ['name' => 'Log Cleanup', 'schedule' => '0 0 * * 0', 'command' => 'find /home/david/logs -name "*.log" -mtime +30 -delete'],
            ],
            'alex' => [
                ['name' => 'WordPress Cron', 'schedule' => '*/5 * * * *', 'command' => 'wp cron event run --due-now --path=/home/alex/domains/docs.enterprise-app.com/public_html'],
                ['name' => 'Health Check', 'schedule' => '*/10 * * * *', 'command' => 'curl -sf https://enterprise-app.com/health > /dev/null'],
                ['name' => 'Cache Warmup', 'schedule' => '0 */6 * * *', 'command' => 'curl -sf https://enterprise-app.com/api/cache/warmup > /dev/null'],
            ],
        ];

        foreach ($cronData as $username => $jobs) {
            $user = $users[$username] ?? null;
            if (! $user) {
                continue;
            }

            foreach ($jobs as $job) {
                CronJob::firstOrCreate(
                    ['user_id' => $user->id, 'name' => $job['name']],
                    [
                        'schedule' => $job['schedule'],
                        'command' => $job['command'],
                        'type' => 'custom',
                        'is_active' => true,
                    ]
                );
            }
        }
    }

    /** @param array<string, User> $users */
    private function seedAuditLogs(array $users): void
    {
        $events = [
            ['action' => 'user.login', 'description' => 'User logged in'],
            ['action' => 'domain.create', 'description' => 'Domain created'],
            ['action' => 'email.create', 'description' => 'Mailbox created'],
            ['action' => 'ssl.issued', 'description' => 'SSL certificate issued'],
            ['action' => 'wordpress.install', 'description' => 'WordPress installed'],
            ['action' => 'database.create', 'description' => 'Database created'],
            ['action' => 'dns.update', 'description' => 'DNS record updated'],
            ['action' => 'service.restart', 'description' => 'Service restarted'],
        ];

        foreach ($users as $user) {
            for ($i = 0; $i < 5; $i++) {
                $event = $events[array_rand($events)];
                AuditLog::create([
                    'user_id' => $user->id,
                    'action' => $event['action'],
                    'description' => $event['description'],
                    'ip_address' => '203.0.113.'.rand(1, 254),
                    'created_at' => now()->subHours(rand(1, 720)),
                ]);
            }
        }
    }
}
