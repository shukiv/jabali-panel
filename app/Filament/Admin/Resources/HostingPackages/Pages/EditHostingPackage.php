<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\HostingPackages\Pages;

use App\Filament\Admin\Resources\HostingPackages\HostingPackageResource;
use App\Services\Agent\AgentClient;
use Filament\Actions\DeleteAction;
use Filament\Resources\Pages\EditRecord;

class EditHostingPackage extends EditRecord
{
    protected static string $resource = HostingPackageResource::class;

    protected function afterSave(): void
    {
        $agent = app(AgentClient::class);
        $allUsers = $this->record->users()->get();

        foreach ($allUsers as $user) {
            // Sync disk quota (users without a per-user override inherit from package)
            if ($user->disk_quota_mb === null || $user->disk_quota_mb === $this->record->getOriginal('disk_quota_mb')) {
                $user->update(['disk_quota_mb' => $this->record->disk_quota_mb]);
                try {
                    $agent->quotaSet($user->username, (int) ($this->record->disk_quota_mb ?? 0));
                } catch (\Throwable) {
                }
            }

            // Sync shell mode (users without a per-user override)
            if ($user->ssh_isolation_mode === null) {
                try {
                    $mode = $this->record->ssh_isolation_mode;
                    if ($mode === 'disabled') {
                        $agent->send('ssh.disable_shell', ['username' => $user->username]);
                    } else {
                        $agent->send('ssh.enable_shell', ['username' => $user->username]);
                        $agent->sshSetShellMode($user->username, $mode);
                    }
                } catch (\Throwable) {
                }
            }

            // Sync cgroup resource limits
            try {
                $limits = $user->getEffectiveResourceLimits();
                if (empty($limits)) {
                    $agent->cgroupRemove($user->username);
                } else {
                    $agent->cgroupApply($user->username, $limits);
                }
            } catch (\Throwable) {
            }
        }

        // Regenerate nginx vhosts to pick up rate limit changes
        $nginxLimitsChanged = $this->record->wasChanged(['nginx_req_per_sec', 'nginx_connections']);
        if ($nginxLimitsChanged) {
            try {
                $agent->send('nginx.regenerate_domain_vhosts');
            } catch (\Throwable) {
            }
        }
    }

    protected function getHeaderActions(): array
    {
        return [
            DeleteAction::make(),
        ];
    }
}
