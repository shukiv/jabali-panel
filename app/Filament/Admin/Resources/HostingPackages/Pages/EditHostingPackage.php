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

        // Sync shell mode for all users that inherit from this package
        $users = $this->record->users()
            ->whereNull('ssh_isolation_mode')
            ->get();

        $mode = $this->record->ssh_isolation_mode;

        foreach ($users as $user) {
            try {
                if ($mode === 'disabled') {
                    $agent->send('ssh.disable_shell', ['username' => $user->username]);
                } else {
                    $agent->send('ssh.enable_shell', ['username' => $user->username]);
                    $agent->sshSetShellMode($user->username, $mode);
                }
            } catch (\Throwable) {
                // Best-effort
            }
        }

        // Sync cgroup limits for all users on this package
        $this->syncCgroupLimitsForPackageUsers($agent);
    }

    private function syncCgroupLimitsForPackageUsers(AgentClient $agent): void
    {
        $allUsers = $this->record->users()->get();

        foreach ($allUsers as $user) {
            $limits = $user->getEffectiveResourceLimits();

            try {
                if (empty($limits)) {
                    $agent->cgroupRemove($user->username);
                } else {
                    $agent->cgroupApply($user->username, $limits);
                }
            } catch (\Throwable) {
                // Best-effort
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
