<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\Users\Pages;

use App\Filament\Admin\Resources\Users\UserResource;
use App\Models\HostingPackage;
use App\Services\Agent\AgentClient;
use App\Services\System\LinuxUserService;
use Exception;
use Filament\Notifications\Notification;
use Filament\Resources\Pages\CreateRecord;

class CreateUser extends CreateRecord
{
    protected static string $resource = UserResource::class;

    protected ?HostingPackage $selectedPackage = null;

    protected function mutateFormDataBeforeCreate(array $data): array
    {
        // Generate SFTP password (same as user password or random)
        if (! empty($data['password'])) {
            $data['sftp_password'] = $data['password'];
        }

        if (! empty($data['hosting_package_id'])) {
            $this->selectedPackage = HostingPackage::find($data['hosting_package_id']);
            $data['disk_quota_mb'] = $this->selectedPackage?->disk_quota_mb;
        } else {
            $this->selectedPackage = null;
            $data['disk_quota_mb'] = null;
        }

        return $data;
    }

    protected function afterCreate(): void
    {
        $createLinuxUser = $this->data['create_linux_user'] ?? true;

        if ($createLinuxUser) {
            try {
                $linuxService = new LinuxUserService;

                // Get the plain password before it was hashed
                $password = $this->data['sftp_password'] ?? null;

                $linuxService->createUser($this->record, $password);

                Notification::make()
                    ->title(__('Linux user created'))
                    ->body(__("System user ':username' has been created.", ['username' => $this->record->username]))
                    ->success()
                    ->send();

                // Apply disk quota if enabled
                $this->applyDiskQuota();
            } catch (Exception $e) {
                Notification::make()
                    ->title(__('Linux user creation failed'))
                    ->body($e->getMessage())
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

    protected function applyDiskQuota(): void
    {
        $quotaMb = $this->record->disk_quota_mb;
        if (! $quotaMb || $quotaMb <= 0) {
            return;
        }

        // Always try to apply quota when set
        try {
            $agent = new AgentClient;
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
