<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\Users\Pages;

use App\Filament\Admin\Resources\Users\UserResource;
use App\Models\HostingPackage;
use App\Services\Agent\AgentClient;
use App\Services\System\LinuxUserService;
use Exception;
use Filament\Actions;
use Filament\Forms\Components\Toggle;
use Filament\Notifications\Notification;
use Filament\Resources\Pages\EditRecord;

class EditUser extends EditRecord
{
    protected static string $resource = UserResource::class;

    protected ?int $originalQuota = null;

    protected ?HostingPackage $selectedPackage = null;

    protected function mutateFormDataBeforeFill(array $data): array
    {
        $this->originalQuota = $data['disk_quota_mb'] ?? null;

        return $data;
    }

    protected function mutateFormDataBeforeSave(array $data): array
    {
        if (! empty($data['hosting_package_id'])) {
            $this->selectedPackage = HostingPackage::find($data['hosting_package_id']);
            $data['disk_quota_mb'] = $this->selectedPackage?->disk_quota_mb;
        } else {
            $this->selectedPackage = null;
            $data['disk_quota_mb'] = null;
        }

        return $data;
    }

    protected function afterSave(): void
    {
        $newQuota = $this->record->disk_quota_mb;
        if ($newQuota !== $this->originalQuota) {
            // Always try to apply quota when changed
            try {
                $agent = new AgentClient;
                $result = $agent->quotaSet($this->record->username, (int) ($newQuota ?? 0));

                if ($result['success'] ?? false) {
                    $message = $newQuota && $newQuota > 0
                        ? __('Quota updated to :size GB', ['size' => number_format($newQuota / 1024, 1)])
                        : __('Quota removed (unlimited)');

                    Notification::make()
                        ->title(__('Disk quota updated'))
                        ->body($message)
                        ->success()
                        ->send();
                } else {
                    throw new Exception($result['error'] ?? __('Unknown error'));
                }
            } catch (Exception $e) {
                // Show warning but don't fail - quota value is saved in database
                Notification::make()
                    ->title(__('Disk quota update failed'))
                    ->body(__('Value saved but filesystem quota not applied: :error', ['error' => $e->getMessage()]))
                    ->warning()
                    ->send();
            }
        }

    }

    protected function getHeaderActions(): array
    {
        return [
            Actions\Action::make('loginAsUser')
                ->label(__('Login as User'))
                ->icon('heroicon-o-arrow-right-on-rectangle')
                ->color('info')
                ->visible(fn () => ! $this->record->is_admin && $this->record->is_active)
                ->url(fn () => route('impersonate.start', ['user' => $this->record->id]), shouldOpenInNewTab: true),
            Actions\DeleteAction::make()
                ->visible(fn () => (int) $this->record->id !== 1)
                ->form([
                    Toggle::make('remove_home')
                        ->label(__('Delete home directory'))
                        ->helperText(__('Warning: This will permanently delete /home/:username and all its contents!', ['username' => $this->record->username]))
                        ->default(false),
                ])
                ->action(function (array $data) {
                    $removeHome = $data['remove_home'] ?? false;
                    $username = $this->record->username;
                    $steps = [];

                    try {
                        $linuxService = new LinuxUserService;
                        $domains = $this->record->domains()->pluck('domain')->all();

                        if ($linuxService->userExists($username)) {
                            $result = $linuxService->deleteUser($username, $removeHome, $domains);

                            if (! ($result['success'] ?? false)) {
                                throw new Exception($result['error'] ?? __('Failed to delete Linux user'));
                            }

                            $steps = array_merge($steps, $result['steps'] ?? []);
                        } else {
                            $steps[] = __('Linux user not found on the server');
                        }
                    } catch (Exception $e) {
                        Notification::make()
                            ->title(__('Linux user deletion failed'))
                            ->body($e->getMessage())
                            ->danger()
                            ->send();
                    }

                    // Delete from database
                    $this->record->delete();

                    $steps[] = __('Removed user from admin list');
                    $details = implode("\n", array_map(fn ($step): string => '• '.$step, $steps));

                    Notification::make()
                        ->title(__('User :username removed', ['username' => $username]))
                        ->body($details)
                        ->success()
                        ->send();

                    $this->redirect($this->getResource()::getUrl('index'));
                }),
        ];
    }
}
