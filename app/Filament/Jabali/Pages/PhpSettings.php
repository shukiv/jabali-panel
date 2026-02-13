<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Services\Agent\AgentClient;
use BackedEnum;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Schema;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Auth;

class PhpSettings extends Page implements HasActions, HasForms
{
    use InteractsWithActions;
    use InteractsWithForms;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-cog-6-tooth';

    protected static ?int $navigationSort = 5;

    public static function getNavigationLabel(): string
    {
        return __('PHP Settings');
    }

    public function getTitle(): string|Htmlable
    {
        return __('PHP Settings');
    }

    protected static ?string $slug = 'php-settings';

    protected string $view = 'filament.jabali.pages.php-settings';

    public array $domains = [];

    public ?string $selectedDomain = null;

    public array $phpVersions = [];

    // PHP Settings form data
    public ?array $data = [];

    protected ?AgentClient $agent = null;

    protected function getAgent(): AgentClient
    {
        if ($this->agent === null) {
            $this->agent = new AgentClient;
        }

        return $this->agent;
    }

    protected function getUsername(): string
    {
        return Auth::user()->username ?? Auth::user()->name ?? 'unknown';
    }

    public function mount(): void
    {
        $this->loadDomains();
        $this->loadPhpVersions();

        if (! empty($this->domains)) {
            $this->selectedDomain = $this->domains[0]['domain'] ?? null;
            $this->loadSettings();
        }

        $this->form->fill($this->data);
    }

    protected function loadDomains(): void
    {
        if ((bool) env('JABALI_DEMO', false)) {
            $this->domains = [
                ['domain' => 'jabali-panel.com'],
                ['domain' => 'demo-site.com'],
                ['domain' => 'store.demo'],
            ];

            return;
        }

        $result = $this->getAgent()->send('domain.list', [
            'username' => $this->getUsername(),
        ]);

        $this->domains = ($result['success'] ?? false) ? ($result['domains'] ?? []) : [];
    }

    protected function loadPhpVersions(): void
    {
        if ((bool) env('JABALI_DEMO', false)) {
            $this->phpVersions = [
                '8.4' => 'PHP 8.4',
                '8.3' => 'PHP 8.3',
                '8.2' => 'PHP 8.2',
                '8.1' => 'PHP 8.1',
            ];

            return;
        }

        $result = $this->getAgent()->send('php.list_versions', []);

        $this->phpVersions = [];
        if ($result['success'] ?? false) {
            foreach ($result['versions'] ?? [] as $v) {
                $version = $v['version'] ?? $v;
                $this->phpVersions[$version] = "PHP $version";
            }
        }

        if (empty($this->phpVersions)) {
            $this->phpVersions = ['8.4' => 'PHP 8.4'];
        }
    }

    public function selectDomain(string $domain): void
    {
        $this->selectedDomain = $domain;
        $this->loadSettings();
        $this->form->fill($this->data);
    }

    public function loadSettings(): void
    {
        if (! $this->selectedDomain) {
            return;
        }

        if ((bool) env('JABALI_DEMO', false)) {
            $this->data = [
                'php_version' => array_key_first($this->phpVersions),
                'memory_limit' => '256M',
                'upload_max_filesize' => '64M',
                'post_max_size' => '64M',
                'max_input_vars' => '3000',
                'max_execution_time' => '300',
                'max_input_time' => '300',
            ];

            return;
        }

        $result = $this->getAgent()->send('php.getSettings', [
            'domain' => $this->selectedDomain,
            'username' => $this->getUsername(),
        ]);

        if ($result['success'] ?? false) {
            $settings = $result['settings'] ?? [];
            $this->data = [
                'php_version' => $settings['php_version'] ?? array_key_first($this->phpVersions),
                'memory_limit' => $settings['memory_limit'] ?? '256M',
                'upload_max_filesize' => $settings['upload_max_filesize'] ?? '64M',
                'post_max_size' => $settings['post_max_size'] ?? '64M',
                'max_input_vars' => $settings['max_input_vars'] ?? '3000',
                'max_execution_time' => $settings['max_execution_time'] ?? '300',
                'max_input_time' => $settings['max_input_time'] ?? '300',
            ];
        } else {
            $this->data = [
                'php_version' => array_key_first($this->phpVersions),
                'memory_limit' => '256M',
                'upload_max_filesize' => '64M',
                'post_max_size' => '64M',
                'max_input_vars' => '3000',
                'max_execution_time' => '300',
                'max_input_time' => '300',
            ];
        }
    }

    public function form(Schema $form): Schema
    {
        return $form
            ->schema([
                Section::make(__('PHP Version'))
                    ->description(__('Choose which PHP version to use for this domain.'))
                    ->icon('heroicon-o-code-bracket')
                    ->schema([
                        Select::make('php_version')
                            ->label(__('PHP Version'))
                            ->options($this->phpVersions)
                            ->required()
                            ->helperText(__('Recommended: Use the latest stable PHP version for best security and performance.')),
                    ]),

                Section::make(__('Resource Limits'))
                    ->description(__('Configure memory and upload limits for your PHP applications.'))
                    ->icon('heroicon-o-cpu-chip')
                    ->schema([
                        Grid::make(2)
                            ->schema([
                                Select::make('memory_limit')
                                    ->label(__('Memory Limit'))
                                    ->options([
                                        '64M' => '64 MB',
                                        '128M' => '128 MB',
                                        '256M' => '256 MB',
                                        '512M' => '512 MB',
                                        '1024M' => '1024 MB',
                                        '2048M' => '2048 MB',
                                    ])
                                    ->required()
                                    ->helperText(__('Maximum memory a script can allocate.')),

                                Select::make('upload_max_filesize')
                                    ->label(__('Upload Max Filesize'))
                                    ->options([
                                        '2M' => '2 MB',
                                        '8M' => '8 MB',
                                        '16M' => '16 MB',
                                        '32M' => '32 MB',
                                        '64M' => '64 MB',
                                        '128M' => '128 MB',
                                        '256M' => '256 MB',
                                        '512M' => '512 MB',
                                    ])
                                    ->required()
                                    ->helperText(__('Maximum size of uploaded files.')),

                                Select::make('post_max_size')
                                    ->label(__('Post Max Size'))
                                    ->options([
                                        '8M' => '8 MB',
                                        '16M' => '16 MB',
                                        '32M' => '32 MB',
                                        '64M' => '64 MB',
                                        '128M' => '128 MB',
                                        '256M' => '256 MB',
                                        '512M' => '512 MB',
                                    ])
                                    ->required()
                                    ->helperText(__('Maximum size of POST data.')),

                                Select::make('max_input_vars')
                                    ->label(__('Max Input Vars'))
                                    ->options([
                                        '1000' => '1,000',
                                        '2000' => '2,000',
                                        '3000' => '3,000',
                                        '5000' => '5,000',
                                        '10000' => '10,000',
                                    ])
                                    ->required()
                                    ->helperText(__('Maximum number of input variables.')),
                            ]),
                    ]),

                Section::make(__('Execution Limits'))
                    ->description(__('Set maximum execution and input processing time for PHP scripts.'))
                    ->icon('heroicon-o-clock')
                    ->schema([
                        Grid::make(2)
                            ->schema([
                                Select::make('max_execution_time')
                                    ->label(__('Max Execution Time'))
                                    ->options([
                                        '30' => '30 '.__('seconds'),
                                        '60' => '60 '.__('seconds'),
                                        '120' => '120 '.__('seconds'),
                                        '300' => '300 '.__('seconds'),
                                        '600' => '600 '.__('seconds'),
                                        '900' => '900 '.__('seconds'),
                                    ])
                                    ->required()
                                    ->helperText(__('Maximum time a script can run.')),

                                Select::make('max_input_time')
                                    ->label(__('Max Input Time'))
                                    ->options([
                                        '60' => '60 '.__('seconds'),
                                        '120' => '120 '.__('seconds'),
                                        '300' => '300 '.__('seconds'),
                                        '600' => '600 '.__('seconds'),
                                        '900' => '900 '.__('seconds'),
                                    ])
                                    ->required()
                                    ->helperText(__('Maximum time for parsing input data.')),
                            ]),
                    ]),
            ])
            ->statePath('data');
    }

    protected function getHeaderActions(): array
    {
        return [
            $this->saveSettingsAction(),
        ];
    }

    protected function saveSettingsAction(): Action
    {
        return Action::make('saveSettings')
            ->label(__('Save Changes'))
            ->icon('heroicon-o-check')
            ->color('success')
            ->requiresConfirmation()
            ->modalHeading(__('Save PHP Settings'))
            ->modalDescription(__('This will update PHP settings for :domain. PHP-FPM will be reloaded to apply changes.', ['domain' => $this->selectedDomain ?? '']))
            ->modalIcon('heroicon-o-cog-6-tooth')
            ->modalIconColor('warning')
            ->modalSubmitActionLabel(__('Save & Reload PHP'))
            ->visible(fn () => $this->selectedDomain !== null)
            ->action(function (): void {
                $this->save();
            });
    }

    public function save(): void
    {
        $formData = $this->form->getState();

        $result = $this->getAgent()->send('php.setSettings', [
            'domain' => $this->selectedDomain,
            'username' => $this->getUsername(),
            'settings' => [
                'php_version' => $formData['php_version'],
                'memory_limit' => $formData['memory_limit'],
                'upload_max_filesize' => $formData['upload_max_filesize'],
                'post_max_size' => $formData['post_max_size'],
                'max_input_vars' => $formData['max_input_vars'],
                'max_execution_time' => $formData['max_execution_time'],
                'max_input_time' => $formData['max_input_time'],
            ],
        ]);

        if ($result['success'] ?? false) {
            Notification::make()
                ->title(__('PHP settings saved'))
                ->body(__('PHP-FPM has been reloaded to apply your changes.'))
                ->success()
                ->send();
        } else {
            Notification::make()
                ->title(__('Failed to save settings'))
                ->body($result['error'] ?? __('Unknown error'))
                ->danger()
                ->send();
        }
    }
}
