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
                            ->visibility('public'),
                        FileUpload::make('logoDark')
                            ->label(__('Dark Logo'))
                            ->image()
                            ->imagePreviewHeight('80')
                            ->acceptedFileTypes(['image/png', 'image/jpeg', 'image/webp', 'image/svg+xml'])
                            ->maxSize(2048)
                            ->disk('public')
                            ->directory('branding')
                            ->visibility('public'),
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
        $data = $this->form->getState();

        if (empty(trim($data['panel_name'] ?? ''))) {
            Notification::make()->title(__('Panel name cannot be empty'))->danger()->send();

            return;
        }

        $service = app(ServerSettingsService::class);
        $service->saveBranding($data['panel_name']);

        // FileUpload returns path string or array — extract the path
        $lightPath = $this->extractUploadPath($data['logoLight'] ?? null);
        if ($lightPath && $lightPath !== $this->currentLogo) {
            if ($this->currentLogo && Storage::disk('public')->exists($this->currentLogo)) {
                Storage::disk('public')->delete($this->currentLogo);
            }
            DnsSetting::set('custom_logo', $lightPath);
            $this->currentLogo = $lightPath;
        }

        $darkPath = $this->extractUploadPath($data['logoDark'] ?? null);
        if ($darkPath && $darkPath !== $this->currentLogoDark) {
            if ($this->currentLogoDark && Storage::disk('public')->exists($this->currentLogoDark)) {
                Storage::disk('public')->delete($this->currentLogoDark);
            }
            DnsSetting::set('custom_logo_dark', $darkPath);
            $this->currentLogoDark = $darkPath;
        }

        DnsSetting::clearCache();

        // Re-fill form with stored paths so FileUpload shows the saved files
        $this->form->fill([
            'panel_name' => $data['panel_name'],
            'logoLight' => $this->currentLogo,
            'logoDark' => $this->currentLogoDark,
        ]);

        Notification::make()->title(__('Branding updated'))->success()->send();
    }

    private function extractUploadPath(mixed $value): ?string
    {
        if (is_string($value)) {
            return $value;
        }
        if (is_array($value)) {
            // FileUpload returns ['filename.jpg'] or ['uuid' => 'path']
            return collect($value)->flatten()->first();
        }

        return null;
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
