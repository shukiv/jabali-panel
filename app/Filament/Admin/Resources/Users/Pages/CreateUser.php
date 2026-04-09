<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\Users\Pages;

use App\Filament\Admin\Resources\Users\UserResource;
use App\Models\HostingPackage;
use App\Services\Agent\AgentClient;
use App\Services\System\LinuxUserService;
use App\Support\SafeError;
use Exception;
use Filament\Notifications\Notification;
use Filament\Resources\Pages\CreateRecord;

class CreateUser extends CreateRecord
{
    protected static string $resource = UserResource::class;

    protected ?HostingPackage $selectedPackage = null;

    protected ?string $plainPassword = null;

    protected function mutateFormDataBeforeCreate(array $data): array
    {
        // Capture plain-text password from Livewire state before dehydration hashes it
        $this->plainPassword = $this->data['password'] ?? null;

        // Store plain password as SFTP password (encrypted by model cast)
        if (filled($this->plainPassword)) {
            $data['sftp_password'] = $this->plainPassword;
        }

        if (! empty($data['hosting_package_id'])) {
            $this->selectedPackage = HostingPackage::find($data['hosting_package_id']);
            $data['disk_quota_mb'] = $this->selectedPackage?->disk_quota_mb;
        } else {
            $this->selectedPackage = null;
            $data['disk_quota_mb'] = null;
        }

        // Null out resource limit fields that are empty (keep only explicit overrides)
        foreach (['cpu_quota', 'memory_limit_mb', 'io_read_mbps', 'io_write_mbps', 'max_processes', 'nginx_req_per_sec', 'nginx_connections'] as $field) {
            if (blank($data[$field] ?? null)) {
                $data[$field] = null;
            }
        }

        return $data;
    }

    protected function afterCreate(): void
    {
        $createLinuxUser = $this->data['create_linux_user'] ?? true;

        if ($createLinuxUser) {
            try {
                $linuxService = new LinuxUserService;

                $password = $this->plainPassword;

                $linuxService->createUser($this->record, $password);

                Notification::make()
                    ->title(__('Linux user created'))
                    ->body(__("System user ':username' has been created.", ['username' => $this->record->username]))
                    ->success()
                    ->send();

                // Apply disk quota if enabled
                $this->applyDiskQuota();

                // Apply cgroup resource limits
                $this->applyCgroupLimits();

                // Enable SSH shell based on effective isolation mode
                $mode = $this->record->getEffectiveSshIsolationMode();
                if ($mode !== 'disabled') {
                    try {
                        $agent = app(AgentClient::class);
                        $agent->send('ssh.enable_shell', ['username' => $this->record->username]);
                        $agent->sshSetShellMode($this->record->username, $mode);
                    } catch (\Throwable) {
                        // Best-effort — don't fail user creation
                    }
                }
            } catch (Exception $e) {
                Notification::make()
                    ->title(__('Linux user creation failed'))
                    ->body(SafeError::message($e))
                    ->danger()
                    ->send();
            }
        }

        if (! $this->record->hosting_package_id) {
            Notification::make()
                ->title(__('No hosting package selected'))
                ->body(__('This user has unlimited quotas.'))
                ->warning()
                ->send();
        }
    }

    protected function applyCgroupLimits(): void
    {
        $limits = $this->record->getEffectiveResourceLimits();
        if (empty($limits)) {
            return;
        }

        try {
            $agent = app(AgentClient::class);
            $result = $agent->cgroupApply($this->record->username, $limits);

            if ($result['success'] ?? false) {
                Notification::make()
                    ->title(__('Resource limits applied'))
                    ->success()
                    ->send();
            }
        } catch (\Throwable) {
            // Best-effort — agent may not support cgroup.apply yet
        }
    }

    protected function applyDiskQuota(): void
    {
        $quotaMb = $this->record->disk_quota_mb;
        if (! $quotaMb || $quotaMb <= 0) {
            return;
        }

        // Always try to apply quota when set
        try {
            $agent = app(AgentClient::class);
            $result = $agent->quotaSet($this->record->username, (int) $quotaMb);

            if ($result['success'] ?? false) {
                Notification::make()
                    ->title(__('Disk quota applied'))
                    ->body(__("Quota of :quota GB set for ':username'.", ['quota' => number_format($quotaMb / 1024, 1), 'username' => $this->record->username]))
                    ->success()
                    ->send();
            } else {
                throw new Exception($result['error'] ?? __('Unknown error'));
            }
        } catch (Exception $e) {
            // Show warning but don't fail - quota value is saved in database
            Notification::make()
                ->title(__('Disk quota failed'))
                ->body(__('Value saved but filesystem quota not applied: :error', ['error' => $e->getMessage()]))
                ->warning()
                ->send();
        }
    }
}
