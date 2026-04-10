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

    protected ?int $oldDiskQuota = null;

    protected function mutateFormDataBeforeSave(array $data): array
    {
        $this->oldDiskQuota = $this->record->disk_quota_mb;

        return $data;
    }

    protected function afterSave(): void
    {
        $agent = app(AgentClient::class);
        $allUsers = $this->record->users()->get();
        $newDiskQuota = $this->record->disk_quota_mb;

        foreach ($allUsers as $user) {
            // Sync disk quota — update users who inherited the old package value (no per-user override)
            $userQuota = $user->disk_quota_mb;
            if ($userQuota === null || $userQuota === $this->oldDiskQuota) {
                $user->update(['disk_quota_mb' => $newDiskQuota]);
                try {
                    $agent->quotaSet($user->username, (int) ($newDiskQuota ?? 0));
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
