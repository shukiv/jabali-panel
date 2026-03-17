<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Models\Domain;
use App\Models\Mailbox;
use App\Models\NotificationLog;
use App\Models\SslCertificate;
use App\Models\User;
use App\Services\Agent\AgentClient;
use Exception;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\File;
use Illuminate\Support\Facades\Redis;
use Symfony\Component\Process\Process;

class DiagnosticReportCommand extends Command
{
    protected $signature = 'jabali:report
                            {--file : Save encrypted report to /tmp instead of stdout}
                            {--json : Output raw JSON (unencrypted, for panel UI)}';

    protected $description = 'Generate an encrypted diagnostic report for troubleshooting';

    private string $basePath;

    public function __construct()
    {
        parent::__construct();
        $this->basePath = base_path();
    }

    public function handle(): int
    {
        $data = $this->collectDiagnostics();

        if ($this->option('json')) {
            $this->line(json_encode($data, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES));

            return 0;
        }

        try {
            $output = $this->encryptReport($data);
        } catch (Exception $e) {
            $this->error('Encryption failed: '.$e->getMessage());

            return 1;
        }

        if ($this->option('file')) {
            $reportId = $data['report_id'];
            $filePath = "/tmp/jabali-report-{$reportId}.enc";
            File::put($filePath, $output);
            chmod($filePath, 0600);
            $this->line("Report saved to {$filePath}");

            return 0;
        }

        $this->line('===== JABALI DIAGNOSTIC REPORT =====');
        $this->line(chunk_split($output, 76));
        $this->line('===== END REPORT =====');

        return 0;
    }

    private function collectDiagnostics(): array
    {
        $data = [
            'report_id' => bin2hex(random_bytes(4)),
            'generated_at' => now()->toIso8601String(),
            'hostname' => gethostname() ?: 'unknown',
        ];

        // Jabali version
        try {
            $versionFile = $this->basePath.'/VERSION';
            if (File::exists($versionFile)) {
                $content = File::get($versionFile);
                $data['jabali_version'] = preg_match('/VERSION=(.+)/', $content, $m) ? trim($m[1]) : 'unknown';
            } else {
                $data['jabali_version'] = 'unknown';
            }
        } catch (Exception) {
            $data['jabali_version'] = 'unknown';
        }

        // PHP version
        $data['php_version'] = PHP_VERSION;

        // OS
        try {
            $osRelease = '/etc/os-release';
            if (File::exists($osRelease)) {
                $content = File::get($osRelease);
                $data['os'] = preg_match('/PRETTY_NAME="?([^"\n]+)"?/', $content, $m) ? trim($m[1]) : php_uname();
            } else {
                $data['os'] = php_uname();
            }
        } catch (Exception) {
            $data['os'] = php_uname();
        }

        // Server software versions
        try {
            $agent = app(AgentClient::class);
            $data['server_versions'] = $agent->serverVersions();
        } catch (Exception) {
            try {
                $versions = [];
                $cmds = [
                    'nginx' => 'nginx -v 2>&1',
                    'mariadb' => 'mariadb --version 2>&1',
                    'postfix' => 'postconf mail_version 2>&1',
                    'dovecot' => 'dovecot --version 2>&1',
                    'named' => 'named -v 2>&1',
                    'redis' => 'redis-server --version 2>&1',
                    'openssl' => 'openssl version 2>&1',
                ];
                foreach ($cmds as $name => $cmd) {
                    $result = $this->executeCommand($cmd);
                    if ($result['exitCode'] === 0) {
                        $versions[$name] = trim($result['output']);
                    }
                }
                $data['server_versions'] = $versions;
            } catch (Exception) {
                // skip
            }
        }

        // PHP extensions
        $data['php_extensions'] = get_loaded_extensions();

        // Laravel config
        $data['laravel_config'] = [
            'env' => config('app.env'),
            'debug' => config('app.debug'),
            'cache_driver' => config('cache.default'),
            'queue_driver' => config('queue.default'),
            'mail_driver' => config('mail.default'),
            'session_driver' => config('session.driver'),
        ];

        // Scale: domain, user, mailbox counts
        try {
            $data['counts'] = [
                'users' => User::count(),
                'domains' => Domain::count(),
                'mailboxes' => Mailbox::count(),
                'ssl_certificates' => SslCertificate::count(),
                'ssl_expiring_7d' => SslCertificate::where('expires_at', '<=', now()->addDays(7))
                    ->where('expires_at', '>', now())
                    ->count(),
                'ssl_expired' => SslCertificate::where('expires_at', '<=', now())->count(),
            ];
        } catch (Exception) {
            // skip
        }

        // Redis connectivity
        try {
            Redis::ping();
            $data['redis'] = 'connected';
        } catch (Exception) {
            $data['redis'] = 'connection failed';
        }

        // Failed queue jobs
        try {
            $data['failed_jobs'] = DB::table('failed_jobs')->count();
        } catch (Exception) {
            // skip
        }

        // Postfix mail queue
        try {
            $result = $this->executeCommand('postqueue -p 2>&1 | tail -1');
            $data['mail_queue'] = $result['exitCode'] === 0 ? trim($result['output']) : null;
        } catch (Exception) {
            // skip
        }

        // PHP-FPM pools
        try {
            $result = $this->executeCommand('ls /etc/php/*/fpm/pool.d/*.conf 2>/dev/null | wc -l');
            $data['php_fpm_pools'] = $result['exitCode'] === 0 ? (int) trim($result['output']) : null;
        } catch (Exception) {
            // skip
        }

        // Cron / Laravel scheduler
        try {
            $result = $this->executeCommand('crontab -l 2>/dev/null | grep -c "artisan schedule:run"');
            $data['scheduler_cron'] = ((int) trim($result['output'])) > 0;
        } catch (Exception) {
            $data['scheduler_cron'] = false;
        }

        // Services
        try {
            $agent = app(AgentClient::class);
            $result = $agent->send('service.list', ['services' => [
                'nginx', 'mariadb', 'mysql', 'postfix', 'dovecot',
                'named', 'bind9', 'redis-server', 'fail2ban', 'jabali-agent',
            ]]);
            $data['services'] = $result;
        } catch (Exception) {
            try {
                $result = $this->executeCommand('systemctl is-active nginx mariadb postfix dovecot named redis-server fail2ban');
                $data['services'] = $result['output'];
            } catch (Exception) {
                // skip
            }
        }

        // Disk
        try {
            $agent = app(AgentClient::class);
            $data['disk'] = $agent->metricsDisk();
        } catch (Exception) {
            try {
                $result = $this->executeCommand('df -h');
                $data['disk'] = $result['output'];
            } catch (Exception) {
                // skip
            }
        }

        // Load & memory
        try {
            $agent = app(AgentClient::class);
            $data['load_memory'] = $agent->metricsOverview();
        } catch (Exception) {
            try {
                $loadavg = File::exists('/proc/loadavg') ? trim(File::get('/proc/loadavg')) : null;
                $meminfo = File::exists('/proc/meminfo') ? trim(File::get('/proc/meminfo')) : null;
                $data['load_memory'] = ['loadavg' => $loadavg, 'meminfo' => $meminfo];
            } catch (Exception) {
                // skip
            }
        }

        // Laravel log
        try {
            $data['laravel_log'] = $this->tailFile($this->basePath.'/storage/logs/laravel.log', 100);
        } catch (Exception) {
            // skip
        }

        // Health monitor log
        try {
            $data['health_monitor_log'] = $this->tailFile('/var/log/jabali/health-monitor.log', 50);
        } catch (Exception) {
            // skip
        }

        // Nginx error log
        try {
            $data['nginx_error_log'] = $this->tailFile('/var/log/nginx/error.log', 30);
        } catch (Exception) {
            // skip
        }

        // PHP-FPM log
        try {
            $phpVersion = PHP_MAJOR_VERSION.'.'.PHP_MINOR_VERSION;
            $data['php_fpm_log'] = $this->tailFile("/var/log/php{$phpVersion}-fpm.log", 30);
        } catch (Exception) {
            // skip
        }

        // Database
        try {
            DB::select('SELECT 1');
            $data['database'] = 'connected';
        } catch (Exception) {
            $data['database'] = 'connection failed';
        }

        // Notification errors
        try {
            $data['notification_errors'] = NotificationLog::where('status', 'failed')
                ->lastDays(1)
                ->latest()
                ->take(10)
                ->get()
                ->toArray();
        } catch (Exception) {
            // skip
        }

        return $data;
    }

    private function tailFile(string $path, int $lines): ?string
    {
        if (! File::exists($path) || ! is_readable($path)) {
            return null;
        }

        $result = $this->executeCommand("tail -n {$lines} ".escapeshellarg($path));

        return $result['exitCode'] === 0 && $result['output'] !== '' ? $result['output'] : null;
    }

    private function encryptReport(array $data): string
    {
        $publicKeyPath = $this->basePath.'/resources/keys/report.pub';

        if (! File::exists($publicKeyPath)) {
            throw new Exception('Report encryption key not found at resources/keys/report.pub');
        }

        $publicKey = openssl_pkey_get_public(File::get($publicKeyPath));
        if ($publicKey === false) {
            throw new Exception('Failed to load report encryption key');
        }

        $compressed = gzencode(json_encode($data, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES));
        if ($compressed === false) {
            throw new Exception('Failed to compress report data');
        }

        $iv = '';
        $encrypted = '';
        $keys = [];

        $sealed = openssl_seal($compressed, $encrypted, $keys, [$publicKey], 'aes-256-cbc', $iv);
        if ($sealed === false) {
            throw new Exception('Failed to encrypt report data');
        }

        $envelope = json_encode([
            'version' => '1.0',
            'encrypted_key' => base64_encode($keys[0]),
            'encrypted_data' => base64_encode($encrypted),
            'iv' => base64_encode($iv),
        ]);

        return base64_encode($envelope);
    }

    private function executeCommand(string $command, int $timeout = 60): array
    {
        $process = Process::fromShellCommandline($command, $this->basePath);
        $process->setTimeout($timeout);
        $process->run();
        $output = trim($process->getOutput().$process->getErrorOutput());

        return [
            'exitCode' => $process->getExitCode() ?? 1,
            'output' => $output,
        ];
    }
}
