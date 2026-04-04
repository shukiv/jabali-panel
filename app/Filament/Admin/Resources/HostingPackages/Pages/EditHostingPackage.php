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
        if (! $this->record->wasChanged('ssh_isolation_mode')) {
            return;
        }

        // Sync config file for all users on this package that inherit (no per-user override)
        $agent = app(AgentClient::class);
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
    }

    protected function getHeaderActions(): array
    {
        return [
            DeleteAction::make(),
        ];
    }
}
