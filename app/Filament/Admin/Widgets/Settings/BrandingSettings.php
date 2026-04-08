<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets\Settings;

use App\Models\DnsSetting;
use App\Services\Agent\AgentClient;
use App\Services\ServerSettingsService;
use App\Support\SafeError;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\CodeEditor;
use Filament\Forms\Components\CodeEditor\Enums\Language;
use Filament\Forms\Components\FileUpload;
use Filament\Forms\Components\TextInput;
use Filament\Notifications\Notification;
use Filament\Schemas\Components\Actions;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Section;
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

    public string $suspendedPageHtml = '';

    public string $welcomePageHtml = '';

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

        $this->loadPageTemplates();
    }

    public function form(Schema $schema): Schema
    {
        return $schema
            ->statePath('data')
            ->components([
                Section::make(__('Panel Branding'))
                    ->schema([
                        Grid::make(3)
                            ->schema([
                                TextInput::make('panel_name')
                                    ->label(__('Control Panel Name'))
                                    ->placeholder(__('Jabali'))
                                    ->helperText(__('Appears in browser title and navigation')),
                                $this->logoUploadField('logoLight', __('Light Logo')),
                                $this->logoUploadField('logoDark', __('Dark Logo')),
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
                    ]),
            ]);
    }

    public function pagesForm(Schema $schema): Schema
    {
        return $schema
            ->components([
                Section::make(__('Page Templates'))
                    ->description(__('Customize the default pages served by the panel.'))
                    ->schema([
                        Section::make(__('Suspended Page'))
                            ->description(__('HTML shown when a domain is disabled. This page is served with a 503 status code.'))
                            ->collapsed()
                            ->schema([
                                CodeEditor::make('suspendedPageHtml')
                                    ->label(__('Suspended Page HTML'))
                                    ->language(Language::Html)
                                    ->wrap()
                                    ->helperText(__('Full HTML document served when a domain is suspended.')),
                                Actions::make([
                                    Action::make('saveSuspendedPage')
                                        ->label(__('Save Suspended Page'))
                                        ->action('saveSuspendedPage'),
                                    Action::make('restoreSuspendedPage')
                                        ->label(__('Restore Default'))
                                        ->icon('heroicon-o-arrow-uturn-left')
                                        ->color('gray')
                                        ->requiresConfirmation()
                                        ->modalHeading(__('Restore Default Suspended Page'))
                                        ->modalDescription(__('This will replace the current content with the default template. Save to apply.'))
                                        ->action('restoreSuspendedPage'),
                                ]),
                            ]),
                        Section::make(__('Welcome Page Template'))
                            ->description(__('Default index.html for new domains. Use {{DOMAIN}} as a placeholder for the domain name.'))
                            ->collapsed()
                            ->schema([
                                CodeEditor::make('welcomePageHtml')
                                    ->label(__('Welcome Page HTML'))
                                    ->language(Language::Html)
                                    ->wrap()
                                    ->helperText(__('Use {{DOMAIN}} placeholder — it will be replaced with the actual domain name.')),
                                Actions::make([
                                    Action::make('saveWelcomePage')
                                        ->label(__('Save Welcome Page'))
                                        ->action('saveWelcomePage'),
                                    Action::make('restoreWelcomePage')
                                        ->label(__('Restore Default'))
                                        ->icon('heroicon-o-arrow-uturn-left')
                                        ->color('gray')
                                        ->requiresConfirmation()
                                        ->modalHeading(__('Restore Default Welcome Page'))
                                        ->modalDescription(__('This will replace the current content with the default template. Save to apply.'))
                                        ->action('restoreWelcomePage'),
                                ]),
                            ]),
                    ]),
            ]);
    }

    public function saveBranding(): void
    {
        // getState() triggers FileUpload's beforeStateDehydrated → saveUploadedFiles()
        $data = $this->form->getState();

        if (empty(trim($data['panel_name'] ?? ''))) {
            Notification::make()->title(__('Panel name cannot be empty'))->danger()->send();

            return;
        }

        app(ServerSettingsService::class)->saveBranding($data['panel_name']);

        // FileUpload returns stored path after getState() processing
        $lightPath = $data['logoLight'] ?? null;
        if (is_string($lightPath) && $lightPath !== $this->currentLogo) {
            if ($this->currentLogo && Storage::disk('public')->exists($this->currentLogo)) {
                Storage::disk('public')->delete($this->currentLogo);
            }
            DnsSetting::set('custom_logo', $lightPath);
            $this->currentLogo = $lightPath;
        }

        $darkPath = $data['logoDark'] ?? null;
        if (is_string($darkPath) && $darkPath !== $this->currentLogoDark) {
            if ($this->currentLogoDark && Storage::disk('public')->exists($this->currentLogoDark)) {
                Storage::disk('public')->delete($this->currentLogoDark);
            }
            DnsSetting::set('custom_logo_dark', $darkPath);
            $this->currentLogoDark = $darkPath;
        }

        DnsSetting::clearCache();

        Notification::make()->title(__('Branding updated'))->success()->send();

        $this->redirect(request()->header('Referer', '/jabali-admin/server-settings?tab=branding'));
    }

    public function saveSuspendedPage(): void
    {
        if (empty(trim($this->suspendedPageHtml))) {
            Notification::make()->title(__('Suspended page HTML cannot be empty'))->danger()->send();

            return;
        }

        try {
            $result = app(AgentClient::class)->send('branding.save_suspended_page', [
                'html' => $this->suspendedPageHtml,
            ]);

            if ($result['success'] ?? false) {
                Notification::make()->title(__('Suspended page saved'))->success()->send();
            } else {
                Notification::make()->title(__('Failed to save'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to save suspended page'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function saveWelcomePage(): void
    {
        if (empty(trim($this->welcomePageHtml))) {
            Notification::make()->title(__('Welcome page HTML cannot be empty'))->danger()->send();

            return;
        }

        if (! str_contains($this->welcomePageHtml, '{{DOMAIN}}')) {
            Notification::make()->title(__('Template must contain {{DOMAIN}} placeholder'))->danger()->send();

            return;
        }

        try {
            $result = app(AgentClient::class)->send('branding.save_welcome_page', [
                'html' => $this->welcomePageHtml,
            ]);

            if ($result['success'] ?? false) {
                Notification::make()->title(__('Welcome page template saved'))->success()->send();
            } else {
                Notification::make()->title(__('Failed to save'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to save welcome page'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function restoreSuspendedPage(): void
    {
        $stubPath = base_path('stubs/suspended.html');
        if (file_exists($stubPath)) {
            $this->suspendedPageHtml = file_get_contents($stubPath);
            Notification::make()->title(__('Default restored — click Save to apply'))->success()->send();
        } else {
            Notification::make()->title(__('Default template not found'))->danger()->send();
        }
    }

    public function restoreWelcomePage(): void
    {
        $stubPath = base_path('stubs/welcome.html');
        if (file_exists($stubPath)) {
            $this->welcomePageHtml = file_get_contents($stubPath);
            Notification::make()->title(__('Default restored — click Save to apply'))->success()->send();
        } else {
            Notification::make()->title(__('Default template not found'))->danger()->send();
        }
    }

    private function loadPageTemplates(): void
    {
        try {
            $result = app(AgentClient::class)->send('branding.get_suspended_page', []);
            if ($result['success'] ?? false) {
                $this->suspendedPageHtml = $result['html'] ?? '';
            }
        } catch (Exception) {
            // Agent not available — leave empty
        }

        try {
            $result = app(AgentClient::class)->send('branding.get_welcome_page', []);
            if ($result['success'] ?? false) {
                $this->welcomePageHtml = $result['html'] ?? '';
            }
        } catch (Exception) {
            // Agent not available — leave empty
        }
    }

    private function logoUploadField(string $name, string $label): FileUpload
    {
        return FileUpload::make($name)
            ->label($label)
            ->image()
            ->imagePreviewHeight('80')
            ->acceptedFileTypes(['image/png', 'image/jpeg', 'image/webp', 'image/svg+xml'])
            ->maxSize(2048)
            ->disk('public')
            ->directory('branding')
            ->visibility('public')
            ->getUploadedFileUsing(static function (FileUpload $component, string $file): ?array {
                $storage = $component->getDisk();

                try {
                    if (! $storage->exists($file)) {
                        return null;
                    }
                } catch (\Throwable) {
                    return null;
                }

                // Use request host for URL to avoid CORS (APP_URL has IP, user accesses via hostname)
                $scheme = request()->getScheme();
                $host = request()->getHttpHost();

                return [
                    'name' => basename($file),
                    'size' => $storage->size($file),
                    'type' => $storage->mimeType($file),
                    'url' => "{$scheme}://{$host}/storage/{$file}",
                ];
            });
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
