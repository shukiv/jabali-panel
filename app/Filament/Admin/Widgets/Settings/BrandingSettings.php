<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets\Settings;

use App\Models\DnsSetting;
use App\Services\ServerSettingsService;
use App\Support\SafeError;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\FileUpload;
use Filament\Forms\Components\TextInput;
use Filament\Notifications\Notification;
use Filament\Schemas\Components\Actions;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Schemas\Schema;
use Illuminate\Contracts\View\View;
use Illuminate\Support\Facades\Storage;
use Livewire\Component;

class BrandingSettings extends Component implements HasActions, HasSchemas
{
    use InteractsWithActions;
    use InteractsWithSchemas;

    public ?array $data = [];

    public ?string $currentLogo = null;

    public ?string $currentLogoDark = null;

    public function mount(): void
    {
        $settings = DnsSetting::getAll();
        $this->currentLogo = $settings['custom_logo'] ?? null;
        $this->currentLogoDark = $settings['custom_logo_dark'] ?? null;

        $this->form->fill([
            'panel_name' => $settings['panel_name'] ?? 'Jabali',
            'logoLight' => $this->currentLogo,
            'logoDark' => $this->currentLogoDark,
        ]);
    }

    public function form(Schema $schema): Schema
    {
        return $schema
            ->statePath('data')
            ->components([
                TextInput::make('panel_name')
                    ->label(__('Control Panel Name'))
                    ->placeholder(__('Jabali'))
                    ->helperText(__('Appears in browser title and navigation')),
                Grid::make(2)
                    ->schema([
                        FileUpload::make('logoLight')
                            ->label(__('Light Logo'))
                            ->image()
                            ->imagePreviewHeight('80')
                            ->acceptedFileTypes(['image/png', 'image/jpeg', 'image/webp', 'image/svg+xml'])
                            ->maxSize(2048)
                            ->disk('public')
                            ->directory('branding')
                            ->visibility('public')
                            ->afterStateUpdated(function ($state) {
                                if ($state) {
                                    $path = is_array($state) ? collect($state)->flatten()->first() : $state;
                                    if ($path && is_string($path)) {
                                        if ($this->currentLogo && Storage::disk('public')->exists($this->currentLogo)) {
                                            Storage::disk('public')->delete($this->currentLogo);
                                        }
                                        DnsSetting::set('custom_logo', $path);
                                        DnsSetting::clearCache();
                                        $this->currentLogo = $path;
                                    }
                                }
                            }),
                        FileUpload::make('logoDark')
                            ->label(__('Dark Logo'))
                            ->image()
                            ->imagePreviewHeight('80')
                            ->acceptedFileTypes(['image/png', 'image/jpeg', 'image/webp', 'image/svg+xml'])
                            ->maxSize(2048)
                            ->disk('public')
                            ->directory('branding')
                            ->visibility('public')
                            ->afterStateUpdated(function ($state) {
                                if ($state) {
                                    $path = is_array($state) ? collect($state)->flatten()->first() : $state;
                                    if ($path && is_string($path)) {
                                        if ($this->currentLogoDark && Storage::disk('public')->exists($this->currentLogoDark)) {
                                            Storage::disk('public')->delete($this->currentLogoDark);
                                        }
                                        DnsSetting::set('custom_logo_dark', $path);
                                        DnsSetting::clearCache();
                                        $this->currentLogoDark = $path;
                                    }
                                }
                            }),
                    ]),
                Actions::make([
                    Action::make('saveBranding')
                        ->label(__('Save Branding'))
                        ->action('saveBranding'),
                    Action::make('removeLogo')
                        ->label(__('Remove Logos'))
                        ->icon('heroicon-o-trash')
                        ->color('danger')
                        ->requiresConfirmation()
                        ->modalHeading(__('Remove Logos'))
                        ->modalDescription(__('Are you sure you want to remove the logos?'))
                        ->action('removeLogo')
                        ->visible(fn (): bool => $this->currentLogo !== null || $this->currentLogoDark !== null),
                ]),
            ]);
    }

    public function saveBranding(): void
    {
        $panelName = $this->data['panel_name'] ?? '';

        if (empty(trim($panelName))) {
            Notification::make()->title(__('Panel name cannot be empty'))->danger()->send();

            return;
        }

        app(ServerSettingsService::class)->saveBranding($panelName);
        DnsSetting::clearCache();

        Notification::make()->title(__('Panel name updated'))->body(__('Refresh to see changes.'))->success()->send();
    }

    public function removeLogo(): void
    {
        try {
            if ($this->currentLogo && Storage::disk('public')->exists($this->currentLogo)) {
                Storage::disk('public')->delete($this->currentLogo);
            }
            if ($this->currentLogoDark && Storage::disk('public')->exists($this->currentLogoDark)) {
                Storage::disk('public')->delete($this->currentLogoDark);
            }
            DnsSetting::set('custom_logo', null);
            DnsSetting::set('custom_logo_dark', null);
            DnsSetting::clearCache();
            $this->currentLogo = null;
            $this->currentLogoDark = null;

            $this->form->fill([
                'panel_name' => $this->data['panel_name'] ?? 'Jabali',
                'logoLight' => null,
                'logoDark' => null,
            ]);

            Notification::make()->title(__('Logos removed'))->success()->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to remove logo'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function render(): View
    {
        return view('filament.admin.widgets.branding-settings');
    }
}
