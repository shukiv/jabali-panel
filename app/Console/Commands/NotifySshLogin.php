<?php

declare(strict_types=1);

namespace App\Console\Commands;

use App\Services\AdminNotificationService;
use Illuminate\Console\Command;

class NotifySshLogin extends Command
{
    protected $signature = 'notify:ssh-login {username} {ip} {--method=password : Authentication method}';

    protected $description = 'Send notification for successful SSH login';

    public function handle(): int
    {
        $username = $this->argument('username');
        $ip = $this->argument('ip');
        $method = $this->option('method');

        AdminNotificationService::sshLogin($username, $ip, $method);

        return Command::SUCCESS;
    }
}
