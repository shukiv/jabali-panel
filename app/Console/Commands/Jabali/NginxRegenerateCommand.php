<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Models\DnsSetting;
use App\Services\Agent\AgentClient;
use Exception;
use Illuminate\Console\Command;

class NginxRegenerateCommand extends Command
{
    protected $signature = 'jabali:nginx:regenerate
        {--panel-only : Only regenerate the panel vhost}
        {--domain= : Regenerate a single domain vhost}';

    protected $description = 'Regenerate nginx vhost configurations from current templates';

    public function handle(): int
    {
        $agent = app(AgentClient::class);
        $panelOnly = $this->option('panel-only');
        $domain = $this->option('domain');
        $failed = false;

        // Always regenerate panel vhost unless --domain is specified
        if (! $domain) {
            $this->info('Regenerating panel vhost...');

            try {
                $customDirectives = DnsSetting::get('panel_nginx_directives') ?? '';
                $result = $agent->send('nginx.regenerate_panel_vhost', [
                    'custom_directives' => $customDirectives,
                ]);

                if ($result['success'] ?? false) {
                    if ($result['unchanged'] ?? false) {
                        $this->info("Panel vhost unchanged for {$result['hostname']}");
                    } else {
                        $this->info("Panel vhost regenerated for {$result['hostname']}");
                        if (! empty($result['backup'])) {
                            $this->line("  Backup: {$result['backup']}");
                        }
                    }
                } else {
                    $this->error('Panel vhost regeneration failed: '.($result['error'] ?? 'Unknown error'));
                    $failed = true;
                }
            } catch (Exception $e) {
                $this->error('Agent error: '.$e->getMessage());
                $failed = true;
            }
        }

        // Regenerate domain vhosts unless --panel-only
        if (! $panelOnly) {
            $this->info($domain ? "Regenerating vhost for {$domain}..." : 'Regenerating all domain vhosts...');

            try {
                $params = [];
                if ($domain) {
                    $params['domain'] = $domain;
                }

                $result = $agent->send('nginx.regenerate_domain_vhosts', $params);

                if ($result['success'] ?? false) {
                    $this->info("Regenerated {$result['regenerated']} domain vhost(s)");
                    if (! empty($result['errors'])) {
                        foreach ($result['errors'] as $error) {
                            $this->warn("  Warning: {$error}");
                        }
                    }
                } else {
                    $this->error('Domain vhost regeneration failed: '.($result['error'] ?? 'Unknown error'));
                    if (! empty($result['errors'])) {
                        foreach ($result['errors'] as $error) {
                            $this->warn("  {$error}");
                        }
                    }
                    $failed = true;
                }
            } catch (Exception $e) {
                $this->error('Agent error: '.$e->getMessage());
                $failed = true;
            }
        }

        return $failed ? self::FAILURE : self::SUCCESS;
    }
}
