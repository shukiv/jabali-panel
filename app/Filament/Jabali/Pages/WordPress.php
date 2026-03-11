<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Models\Domain;
use App\Models\DnsRecord;
use App\Models\DnsSetting;
use App\Models\MysqlCredential;
use App\Jobs\RunWpscanScan;
use App\Services\Agent\AgentClient;
use App\Support\ServerFacts;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\ActionGroup;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Infolists\Components\TextEntry;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Columns\ViewColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Facades\Crypt;

class WordPress extends Page implements HasActions, HasForms, HasTable
{
    protected static ?string $slug = 'wordpress';

    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-pencil-square';

    protected static ?int $navigationSort = 4;

    public static function getNavigationLabel(): string
    {
        return __('WordPress');
    }

    protected string $view = 'filament.jabali.pages.wordpress';

    public array $sites = [];

    public array $domains = [];

    public ?string $selectedSiteId = null;

    // Credentials modal
    public bool $showCredentials = false;

    public array $credentials = [];

    // Scan modal
    public bool $showScanModal = false;

    public array $scannedSites = [];

    public bool $isScanning = false;

    // Security scan
    public bool $showSecurityScanModal = false;

    public array $securityScanResults = [];

    public bool $isSecurityScanning = false;

    public ?string $scanningSiteId = null;

    public ?string $scanningSiteUrl = null;

    protected ?AgentClient $agent = null;

    public function getTitle(): string|Htmlable
    {
        return __('WordPress Manager');
    }

    public function getAgent(): AgentClient
    {
        if ($this->agent === null) {
            $this->agent = new AgentClient;
        }

        return $this->agent;
    }

    public function getUsername(): string
    {
        return Auth::user()->username;
    }

    public function mount(): void
    {
        $this->loadData();
    }

    public function loadData(): void
    {
        // Load WordPress sites
        try {
            $result = $this->getAgent()->wpList($this->getUsername());
            $this->sites = $result['sites'] ?? [];
        } catch (Exception $e) {
            $this->sites = [];
        }

        // Load domains for the install form
        try {
            $result = $this->getAgent()->domainList($this->getUsername());
            $this->domains = $result['domains'] ?? [];
        } catch (Exception $e) {
            $this->domains = [];
        }
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => collect($this->sites)->keyBy('id')->toArray())
            ->columns([
                ViewColumn::make('screenshot')
                    ->label(__('Preview'))
                    ->view('filament.jabali.columns.wordpress-screenshot'),
                ViewColumn::make('domain')
                    ->label(__('Site'))
                    ->view('filament.jabali.columns.wordpress-site'),
                TextColumn::make('cache_enabled')
                    ->label(__('Cache'))
                    ->badge()
                    ->formatStateUsing(fn (bool $state): string => $state ? __('On') : __('Off'))
                    ->color(fn (bool $state): string => $state ? 'success' : 'gray'),
                TextColumn::make('debug_enabled')
                    ->label(__('Debug'))
                    ->badge()
                    ->getStateUsing(fn (array $record): bool => $record['debug_enabled'] ?? false)
                    ->formatStateUsing(fn (bool $state): string => $state ? __('On') : __('Off'))
                    ->color(fn (bool $state): string => $state ? 'warning' : 'gray'),
                TextColumn::make('auto_update')
                    ->label(__('Updates'))
                    ->badge()
                    ->getStateUsing(fn (array $record): bool => $record['auto_update'] ?? false)
                    ->formatStateUsing(fn (bool $state): string => $state ? __('Auto') : __('Manual'))
                    ->color(fn (bool $state): string => $state ? 'success' : 'gray'),
            ])
            ->recordActions([
                Action::make('admin')
                    ->label(__('Admin'))
                    ->icon('heroicon-o-arrow-right-on-rectangle')
                    ->action(fn (array $record) => $this->autoLogin($record['id'])),
                ActionGroup::make([
                    Action::make('update')
                        ->label(__('Update Now'))
                        ->icon('heroicon-o-arrow-path')
                        ->action(fn (array $record) => $this->updateWordPress($record['id'])),
                    Action::make('cache')
                        ->label(fn (array $record): string => ($record['cache_enabled'] ?? false) ? __('Disable Cache') : __('Enable Cache'))
                        ->icon('heroicon-o-bolt')
                        ->modalHeading(fn (array $record): string => ($record['cache_enabled'] ?? false) ? __('Disable Jabali Cache') : __('Enable Jabali Cache'))
                        ->modalDescription(fn (array $record): string => ($record['cache_enabled'] ?? false)
                            ? __('This will disable caching for this WordPress site.')
                            : __('This will enable Redis object caching and nginx page caching for better performance.'))
                        ->modalWidth('md')
                        ->form(fn (array $record): array => ($record['cache_enabled'] ?? false)
                            ? [
                                Toggle::make('remove_plugin')
                                    ->label(__('Uninstall Jabali Cache plugin'))
                                    ->helperText(__('Completely remove the plugin files from WordPress. If unchecked, the plugin will only be deactivated.'))
                                    ->default(false)
                                    ->live(),
                                Toggle::make('reset_data')
                                    ->label(__('Delete plugin settings'))
                                    ->helperText(__('Remove all Jabali Cache settings from the database.'))
                                    ->default(false)
                                    ->visible(fn ($get) => $get('remove_plugin')),
                            ]
                            : [])
                        ->action(fn (array $record, array $data) => $this->toggleCache($record['id'], $data['remove_plugin'] ?? false, $data['reset_data'] ?? false)),
                    Action::make('debug')
                        ->label(fn (array $record): string => ($record['debug_enabled'] ?? false) ? __('Disable Debug') : __('Enable Debug'))
                        ->icon('heroicon-o-bug-ant')
                        ->color('warning')
                        ->action(fn (array $record) => $this->toggleDebug($record['id'])),
                    Action::make('autoUpdate')
                        ->label(fn (array $record): string => ($record['auto_update'] ?? false) ? __('Disable Auto-Update') : __('Enable Auto-Update'))
                        ->icon('heroicon-o-arrow-path')
                        ->action(fn (array $record) => $this->toggleAutoUpdate($record['id'])),
                    Action::make('staging')
                        ->label(__('Create Staging'))
                        ->icon('heroicon-o-document-duplicate')
                        ->color('info')
                        ->requiresConfirmation()
                        ->modalHeading(__('Create Staging Environment'))
                        ->modalDescription(__('This will create a copy of your site for testing.'))
                        ->modalIcon('heroicon-o-document-duplicate')
                        ->modalIconColor('info')
                        ->form(function (array $record): array {
                            $sourceDomain = strtolower(trim((string) ($record['domain'] ?? '')));
                            $ownedDomainOptions = $this->getOwnedDomainOptions([$sourceDomain]);

                            return [
                                Select::make('staging_target_type')
                                    ->label(__('Target Type'))
                                    ->options([
                                        'subdomain' => __('Subdomain (on source domain)'),
                                        'domain' => __('Existing domain from my list'),
                                    ])
                                    ->default('subdomain')
                                    ->required()
                                    ->native(false)
                                    ->live(),
                                TextInput::make('staging_subdomain')
                                    ->label(__('Subdomain'))
                                    ->suffix(fn (array $record): string => '.'.($record['domain'] ?? ''))
                                    ->default('test')
                                    ->required(fn (Get $get): bool => $get('staging_target_type') !== 'domain')
                                    ->visible(fn (Get $get): bool => $get('staging_target_type') !== 'domain')
                                    ->regex('/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/')
                                    ->helperText(__('Example: "test" creates test.:domain', ['domain' => $record['domain'] ?? ''])),
                                Select::make('staging_domain')
                                    ->label(__('Target Domain'))
                                    ->options($ownedDomainOptions)
                                    ->required(fn (Get $get): bool => $get('staging_target_type') === 'domain')
                                    ->visible(fn (Get $get): bool => $get('staging_target_type') === 'domain')
                                    ->searchable()
                                    ->native(false)
                                    ->placeholder(__('Select a domain...'))
                                    ->helperText(__('Use one of your existing domains as the staging target.')),
                            ];
                        })
                        ->action(fn (array $data, array $record) => $this->createStaging(
                            $record['id'],
                            (string) ($data['staging_subdomain'] ?? ''),
                            (string) ($data['staging_domain'] ?? ''),
                            (string) ($data['staging_target_type'] ?? 'subdomain')
                        )),
                    Action::make('pushStaging')
                        ->label(__('Push to Production'))
                        ->icon('heroicon-o-arrow-up-tray')
                        ->color('warning')
                        ->requiresConfirmation()
                        ->visible(fn (array $record): bool => (bool) ($record['is_staging'] ?? false))
                        ->modalHeading(__('Push Staging to Production'))
                        ->modalDescription(__('This will replace the live site files and database with the staging version.'))
                        ->modalIcon('heroicon-o-arrow-up-tray')
                        ->modalIconColor('warning')
                        ->action(fn (array $record) => $this->pushStaging($record['id'])),
                    Action::make('security')
                        ->label(__('Security Scan'))
                        ->icon('heroicon-o-shield-check')
                        ->action(fn (array $record) => $this->runSecurityScan($record['id'])),
                    Action::make('delete')
                        ->label(__('Delete Site'))
                        ->icon('heroicon-o-trash')
                        ->color('danger')
                        ->requiresConfirmation()
                        ->modalHeading(__('Delete WordPress Site'))
                        ->modalDescription(__('Are you sure you want to delete this WordPress installation? This action cannot be undone.'))
                        ->modalIcon('heroicon-o-trash')
                        ->modalIconColor('danger')
                        ->form([
                            Toggle::make('delete_files')
                                ->label(__('Delete all files'))
                                ->default(true)
                                ->helperText(__('Permanently remove all WordPress files from the server')),
                            Toggle::make('delete_database')
                                ->label(__('Delete database'))
                                ->default(true)
                                ->helperText(__('Permanently delete the WordPress database and all content')),
                        ])
                        ->action(function (array $data, array $record): void {
                            try {
                                $result = $this->getAgent()->wpDelete(
                                    $this->getUsername(),
                                    $record['id'],
                                    $data['delete_files'] ?? true,
                                    $data['delete_database'] ?? true
                                );

                                if ($result['success'] ?? false) {
                                    if (($record['is_staging'] ?? false)) {
                                        $this->removeStagingDnsRecords($record);
                                    }

                                    if (($record['is_staging'] ?? false) && ! empty($record['domain'])) {
                                        Domain::query()
                                            ->where('user_id', Auth::id())
                                            ->where('domain', (string) $record['domain'])
                                            ->delete();
                                    }

                                    // Delete screenshot if exists
                                    $screenshotPath = storage_path('app/public/screenshots/wp-'.$record['id'].'.png');
                                    if (file_exists($screenshotPath)) {
                                        @unlink($screenshotPath);
                                    }

                                    Notification::make()
                                        ->title(__('WordPress Deleted'))
                                        ->success()
                                        ->send();
                                    $this->loadData();
                                    $this->resetTable();
                                } else {
                                    throw new Exception($result['error'] ?? __('Deletion failed'));
                                }
                            } catch (Exception $e) {
                                Notification::make()
                                    ->title(__('Deletion Failed'))
                                    ->body($e->getMessage())
                                    ->danger()
                                    ->send();
                            }
                        }),
                ])
                    ->icon('heroicon-o-ellipsis-vertical')
                    ->color('gray')
                    ->iconButton(),
            ])
            ->emptyStateHeading(__('No WordPress Sites'))
            ->emptyStateDescription(__('Click "Install WordPress" to create your first site'))
            ->emptyStateIcon('heroicon-o-globe-alt');
    }

    public function getTableRecordKey(\Illuminate\Database\Eloquent\Model|array $record): string
    {
        return is_array($record) ? ($record['id'] ?? '') : $record->getKey();
    }

    public function generateSecurePassword(int $length = 16): string
    {
        $lowercase = 'abcdefghijklmnopqrstuvwxyz';
        $uppercase = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ';
        $numbers = '0123456789';
        $special = '!@#$%^&*';

        // Ensure at least one of each required type
        $password = $lowercase[random_int(0, strlen($lowercase) - 1)]
            .$uppercase[random_int(0, strlen($uppercase) - 1)]
            .$numbers[random_int(0, strlen($numbers) - 1)]
            .$special[random_int(0, strlen($special) - 1)];

        // Fill the rest with random characters from all types
        $allChars = $lowercase.$uppercase.$numbers.$special;
        for ($i = strlen($password); $i < $length; $i++) {
            $password .= $allChars[random_int(0, strlen($allChars) - 1)];
        }

        // Shuffle the password to randomize position of required characters
        return str_shuffle($password);
    }

    public function closeCredentials(): void
    {
        $this->showCredentials = false;
        $this->credentials = [];
    }

    protected function getHeaderActions(): array
    {
        return [
            $this->scanAction(),
            $this->installAction(),
        ];
    }

    protected function scanAction(): Action
    {
        return Action::make('scan')
            ->label(__('Scan for Sites'))
            ->icon('heroicon-o-magnifying-glass')
            ->color('gray')
            ->modalHeading(__('Scan for WordPress Sites'))
            ->modalDescription(__('Search your home folder for WordPress installations not yet tracked.'))
            ->modalIcon('heroicon-o-magnifying-glass')
            ->modalIconColor('info')
            ->modalSubmitActionLabel(__('Scan'))
            ->form(function (): array {
                // Perform scan when form is loaded
                $result = $this->getAgent()->wpScan($this->getUsername());
                $this->scannedSites = ($result['success'] ?? false) ? ($result['found'] ?? []) : [];

                if (empty($this->scannedSites)) {
                    return [
                        \Filament\Forms\Components\Placeholder::make('no_sites')
                            ->label('')
                            ->content(__('No untracked WordPress installations found in your home folder.')),
                    ];
                }

                $options = [];
                foreach ($this->scannedSites as $site) {
                    $label = ($site['site_url'] ?? $site['path']).' (v'.($site['version'] ?? '?').')';
                    $options[$site['path']] = $label;
                }

                return [
                    \Filament\Forms\Components\Placeholder::make('found_info')
                        ->label('')
                        ->content(__('Found :count WordPress installation(s). Select which ones to import:', ['count' => count($this->scannedSites)])),
                    \Filament\Forms\Components\CheckboxList::make('sites_to_import')
                        ->label(__('WordPress Sites'))
                        ->options($options)
                        ->default(array_keys($options))
                        ->descriptions(collect($this->scannedSites)->mapWithKeys(fn ($site) => [$site['path'] => $site['path']])->toArray())
                        ->bulkToggleable(),
                ];
            })
            ->action(function (array $data): void {
                $sitesToImport = $data['sites_to_import'] ?? [];

                if (empty($sitesToImport)) {
                    Notification::make()
                        ->title(__('No Sites Selected'))
                        ->body(__('Please select at least one site to import.'))
                        ->warning()
                        ->send();

                    return;
                }

                $imported = 0;
                $failed = 0;

                foreach ($sitesToImport as $path) {
                    try {
                        $result = $this->getAgent()->wpImport($this->getUsername(), $path);
                        if ($result['success'] ?? false) {
                            $imported++;
                        } else {
                            $failed++;
                        }
                    } catch (Exception $e) {
                        $failed++;
                    }
                }

                if ($imported > 0) {
                    Notification::make()
                        ->title(__('Import Complete'))
                        ->body(__(':count site(s) imported successfully.', ['count' => $imported]))
                        ->success()
                        ->send();
                }

                if ($failed > 0) {
                    Notification::make()
                        ->title(__('Some Imports Failed'))
                        ->body(__(':count site(s) could not be imported.', ['count' => $failed]))
                        ->warning()
                        ->send();
                }

                $this->loadData();
                $this->resetTable();
            });
    }

    protected function installAction(): Action
    {
        $domainOptions = [];
        foreach ($this->domains as $domain) {
            $domainOptions[$domain['domain']] = $domain['domain'];
        }

        return Action::make('install')
            ->label(__('Install WordPress'))
            ->icon('heroicon-o-plus-circle')
            ->color('primary')
            ->modalWidth('lg')
            ->modalHeading(__('Install New WordPress Site'))
            ->modalDescription(__('Set up a new WordPress installation on one of your domains'))
            ->modalIcon('heroicon-o-pencil-square')
            ->modalIconColor('primary')
            ->modalSubmitActionLabel(__('Install WordPress'))
            ->form([
                Select::make('domain')
                    ->label(__('Domain'))
                    ->options($domainOptions)
                    ->required()
                    ->searchable()
                    ->live()
                    ->placeholder(__('Select a domain...'))
                    ->helperText(__('The domain where WordPress will be installed')),
                Toggle::make('use_www')
                    ->label(__('Use www prefix'))
                    ->visible(fn (Get $get): bool => filled($get('domain')))
                    ->helperText(__('Install on www.domain.com instead of domain.com'))
                    ->default(false),
                TextInput::make('path')
                    ->label(__('Directory (optional)'))
                    ->visible(fn (Get $get): bool => filled($get('domain')))
                    ->placeholder(__('Leave empty to install in root'))
                    ->helperText(__('e.g., "blog" to install at domain.com/blog')),
                TextInput::make('site_title')
                    ->label(__('Site Title'))
                    ->required(fn (Get $get): bool => filled($get('domain')))
                    ->visible(fn (Get $get): bool => filled($get('domain')))
                    ->default(__('My WordPress Site'))
                    ->helperText(__('The name of your WordPress site')),
                TextInput::make('admin_user')
                    ->label(__('Admin Username'))
                    ->required(fn (Get $get): bool => filled($get('domain')))
                    ->visible(fn (Get $get): bool => filled($get('domain')))
                    ->default('admin')
                    ->alphaNum()
                    ->helperText(__('Username for the WordPress admin account')),
                TextInput::make('admin_password')
                    ->label(__('Admin Password'))
                    ->password()
                    ->revealable()
                    ->required(fn (Get $get): bool => filled($get('domain')))
                    ->visible(fn (Get $get): bool => filled($get('domain')))
                    ->default(fn () => $this->generateSecurePassword())
                    ->minLength(8)
                    ->rules([
                        'regex:/[a-z]/',      // lowercase
                        'regex:/[A-Z]/',      // uppercase
                        'regex:/[0-9]/',      // number
                    ])
                    ->suffixActions([
                        Action::make('generatePassword')
                            ->icon('heroicon-o-arrow-path')
                            ->tooltip(__('Generate secure password'))
                            ->action(fn ($set) => $set('admin_password', $this->generateSecurePassword())),
                        Action::make('copyPassword')
                            ->icon('heroicon-o-clipboard-document')
                            ->tooltip(__('Copy to clipboard'))
                            ->action(function ($state, $livewire) {
                                if ($state) {
                                    $escaped = addslashes($state);
                                    $livewire->js("navigator.clipboard.writeText('{$escaped}')");
                                    Notification::make()
                                        ->title(__('Copied to clipboard'))
                                        ->success()
                                        ->duration(2000)
                                        ->send();
                                }
                            }),
                    ])
                    ->helperText(__('Minimum 8 characters with uppercase, lowercase, and numbers')),
                TextInput::make('admin_email')
                    ->label(__('Admin Email'))
                    ->required(fn (Get $get): bool => filled($get('domain')))
                    ->visible(fn (Get $get): bool => filled($get('domain')))
                    ->email()
                    ->default(Auth::user()->email ?? '')
                    ->helperText(__('Email address for the WordPress admin account')),
                Select::make('language')
                    ->label(__('Language'))
                    ->options([
                        'en_US' => __('English (United States)'),
                        'en_GB' => __('English (UK)'),
                        'es_ES' => __('Español (España)'),
                        'es_MX' => __('Español (México)'),
                        'fr_FR' => __('Français'),
                        'de_DE' => __('Deutsch'),
                        'it_IT' => __('Italiano'),
                        'pt_BR' => __('Português (Brasil)'),
                        'pt_PT' => __('Português (Portugal)'),
                        'ru_RU' => __('Русский'),
                        'ja' => __('日本語'),
                        'zh_CN' => __('中文 (简体)'),
                        'zh_TW' => __('中文 (繁體)'),
                        'ar' => __('العربية'),
                        'he_IL' => __('עברית'),
                        'hi_IN' => __('हिन्दी'),
                        'ko_KR' => __('한국어'),
                        'nl_NL' => __('Nederlands'),
                        'pl_PL' => __('Polski'),
                        'sv_SE' => __('Svenska'),
                        'tr_TR' => __('Türkçe'),
                        'id_ID' => __('Bahasa Indonesia'),
                        'th' => __('ไทย'),
                        'vi' => __('Tiếng Việt'),
                    ])
                    ->default('en_US')
                    ->searchable()
                    ->required(fn (Get $get): bool => filled($get('domain')))
                    ->visible(fn (Get $get): bool => filled($get('domain')))
                    ->helperText(__('Default language for WordPress admin and content')),
                Toggle::make('enable_cache')
                    ->label(__('Enable Jabali Cache'))
                    ->visible(fn (Get $get): bool => filled($get('domain')))
                    ->helperText(__('Install Redis object caching for better performance'))
                    ->default(true),
                Toggle::make('enable_auto_update')
                    ->label(__('Enable Auto-Updates'))
                    ->visible(fn (Get $get): bool => filled($get('domain')))
                    ->helperText(__('Automatically update WordPress, plugins, and themes'))
                    ->default(false),
            ])
            ->action(function (array $data): void {
                try {
                    Notification::make()
                        ->title(__('Installing WordPress...'))
                        ->body(__('This may take a minute.'))
                        ->info()
                        ->send();

                    $result = $this->getAgent()->wpInstall($this->getUsername(), $data['domain'], [
                        'path' => $data['path'] ?? '',
                        'site_title' => $data['site_title'],
                        'admin_user' => $data['admin_user'],
                        'admin_password' => $data['admin_password'],
                        'admin_email' => $data['admin_email'],
                        'use_www' => $data['use_www'] ?? false,
                        'language' => $data['language'] ?? 'en_US',
                    ]);

                    if ($result['success'] ?? false) {
                        $this->credentials = [
                            'url' => $result['url'],
                            'admin_url' => $result['admin_url'],
                            'admin_user' => $result['admin_user'],
                            'admin_password' => $result['admin_password'],
                            'db_name' => $result['db_name'],
                            'db_user' => $result['db_user'],
                            'db_password' => $result['db_password'],
                        ];

                        // Enable Jabali Cache (Redis object cache) if requested
                        // Note: nginx page cache is enabled by default for all WordPress sites
                        if ($data['enable_cache'] ?? false) {
                            $siteId = $result['site_id'] ?? null;
                            if ($siteId) {
                                try {
                                    $this->getAgent()->wpCacheEnable($this->getUsername(), $siteId);
                                    $this->credentials['cache_enabled'] = true;
                                } catch (Exception $e) {
                                    // Cache enable failed, but installation succeeded
                                    $this->credentials['cache_enabled'] = false;
                                    $this->credentials['cache_error'] = $e->getMessage();
                                }
                            }
                        }

                        // Store MySQL credentials for phpMyAdmin SSO
                        if (! empty($result['db_user']) && ! empty($result['db_password'])) {
                            MysqlCredential::updateOrCreate(
                                [
                                    'user_id' => Auth::id(),
                                    'mysql_username' => $result['db_user'],
                                ],
                                [
                                    'mysql_password_encrypted' => Crypt::encryptString($result['db_password']),
                                ]
                            );
                        }

                        Notification::make()
                            ->title(__('WordPress Installed!'))
                            ->success()
                            ->send();

                        $this->loadData();
                        $this->resetTable();

                        // Show credentials modal
                        $this->mountAction('showCredentialsAction');
                    } else {
                        throw new Exception($result['error'] ?? __('Unknown error'));
                    }
                } catch (Exception $e) {
                    Notification::make()
                        ->title(__('Installation Failed'))
                        ->body($e->getMessage())
                        ->danger()
                        ->send();
                }
            });
    }

    public function autoLogin(string $siteId): void
    {
        try {
            $result = $this->getAgent()->wpAutoLogin($this->getUsername(), $siteId);

            if ($result['success'] ?? false) {
                $loginUrl = $result['login_url'];
                $this->js("window.open('{$loginUrl}', '_blank')");
            } else {
                throw new Exception($result['error'] ?? __('Failed to generate login link'));
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Auto-login Failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function deleteSite(string $siteId): void
    {
        $this->selectedSiteId = $siteId;
        $this->mountAction('deleteAction');
    }

    public function deleteAction(): Action
    {
        return Action::make('deleteAction')
            ->requiresConfirmation()
            ->modalHeading(__('Delete WordPress Site'))
            ->modalDescription(__('Are you sure you want to delete this WordPress installation? This action cannot be undone.'))
            ->modalIcon('heroicon-o-trash')
            ->modalIconColor('danger')
            ->modalSubmitActionLabel(__('Delete WordPress Site'))
            ->form([
                Toggle::make('delete_files')
                    ->label(__('Delete all files'))
                    ->default(true)
                    ->helperText(__('Permanently remove all WordPress files from the server')),
                Toggle::make('delete_database')
                    ->label(__('Delete database'))
                    ->default(true)
                    ->helperText(__('Permanently delete the WordPress database and all content')),
            ])
            ->color('danger')
            ->action(function (array $data): void {
                try {
                    $result = $this->getAgent()->wpDelete(
                        $this->getUsername(),
                        $this->selectedSiteId,
                        $data['delete_files'] ?? true,
                        $data['delete_database'] ?? true
                    );

                    if ($result['success'] ?? false) {
                        Notification::make()
                            ->title(__('WordPress Deleted'))
                            ->success()
                            ->send();
                        $this->loadData();
                    } else {
                        throw new Exception($result['error'] ?? __('Deletion failed'));
                    }
                } catch (Exception $e) {
                    Notification::make()
                        ->title(__('Deletion Failed'))
                        ->body($e->getMessage())
                        ->danger()
                        ->send();
                }
            });
    }

    public function toggleCache(string $siteId, bool $removePlugin = false, bool $resetData = false): void
    {
        try {
            // Get site info to get domain
            $site = collect($this->sites)->firstWhere('id', $siteId);
            if (! $site) {
                throw new Exception(__('Site not found'));
            }
            $siteDomain = $site['domain'] ?? '';

            // Get current cache status (Redis object cache)
            $statusResult = $this->getAgent()->wpCacheStatus($this->getUsername(), $siteId);
            $isEnabled = ($statusResult['status']['enabled'] ?? false);

            if ($isEnabled) {
                // Disable Redis object cache and nginx page cache
                $result = $this->getAgent()->wpCacheDisable($this->getUsername(), $siteId, $removePlugin, $resetData);
                if ($result['success'] ?? false) {
                    // Also update Domain model's page_cache_enabled field
                    if ($siteDomain) {
                        Domain::where('domain', $siteDomain)
                            ->where('user_id', Auth::id())
                            ->update(['page_cache_enabled' => false]);
                    }

                    $message = match (true) {
                        $removePlugin && $resetData => __('Jabali Cache has been completely uninstalled and all settings removed.'),
                        $removePlugin => __('Jabali Cache has been disabled and uninstalled from this site.'),
                        default => __('Jabali Cache has been disabled for this site.'),
                    };

                    Notification::make()
                        ->title(__('Cache Disabled'))
                        ->body($message)
                        ->success()
                        ->send();
                } else {
                    throw new Exception($result['error'] ?? __('Failed to disable cache'));
                }
            } else {
                // Enable Redis object cache and nginx page cache
                $result = $this->getAgent()->wpCacheEnable($this->getUsername(), $siteId);
                if ($result['success'] ?? false) {
                    // Also update Domain model's page_cache_enabled field
                    if ($siteDomain) {
                        Domain::where('domain', $siteDomain)
                            ->where('user_id', Auth::id())
                            ->update(['page_cache_enabled' => true]);
                    }

                    Notification::make()
                        ->title(__('Cache Enabled'))
                        ->body(__('Jabali Cache has been enabled. Cache prefix: :prefix', ['prefix' => $result['cache_prefix'] ?? __('unknown')]))
                        ->success()
                        ->send();
                } else {
                    // Check if there are conflicting plugins
                    if (isset($result['conflicts']) && ! empty($result['conflicts'])) {
                        $conflictNames = array_column($result['conflicts'], 'name');
                        Notification::make()
                            ->title(__('Conflicting Plugins Detected'))
                            ->body(__('Please disable these caching plugins first: :plugins', ['plugins' => implode(', ', $conflictNames)]))
                            ->warning()
                            ->persistent()
                            ->send();
                    } else {
                        throw new Exception($result['error'] ?? __('Failed to enable cache'));
                    }
                }
            }

            $this->loadData();
            $this->resetTable();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Cache Toggle Failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function toggleDebug(string $siteId): void
    {
        try {
            $result = $this->getAgent()->send('wp.toggle_debug', [
                'username' => $this->getUsername(),
                'site_id' => $siteId,
            ]);

            if ($result['success'] ?? false) {
                $enabled = $result['debug_enabled'] ?? false;
                Notification::make()
                    ->title($enabled ? __('Debug Mode Enabled') : __('Debug Mode Disabled'))
                    ->body($enabled
                        ? __('WP_DEBUG is now ON. Check wp-content/debug.log for errors.')
                        : __('WP_DEBUG has been turned OFF.'))
                    ->color($enabled ? 'warning' : 'success')
                    ->send();
                $this->loadData();
                $this->resetTable();
            } else {
                throw new Exception($result['error'] ?? __('Failed to toggle debug mode'));
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Debug Toggle Failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function toggleAutoUpdate(string $siteId): void
    {
        try {
            $result = $this->getAgent()->send('wp.toggle_auto_update', [
                'username' => $this->getUsername(),
                'site_id' => $siteId,
            ]);

            if ($result['success'] ?? false) {
                $enabled = $result['auto_update'] ?? false;
                Notification::make()
                    ->title($enabled ? __('Auto-Update Enabled') : __('Auto-Update Disabled'))
                    ->body($enabled
                        ? __('WordPress core, plugins, and themes will be updated automatically.')
                        : __('Automatic updates have been disabled.'))
                    ->success()
                    ->send();
                $this->loadData();
                $this->resetTable();
            } else {
                throw new Exception($result['error'] ?? __('Failed to toggle auto-update'));
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Auto-Update Toggle Failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function updateWordPress(string $siteId): void
    {
        try {
            Notification::make()
                ->title(__('Updating WordPress...'))
                ->body(__('This may take a moment.'))
                ->info()
                ->send();

            $result = $this->getAgent()->send('wp.update', [
                'username' => $this->getUsername(),
                'site_id' => $siteId,
                'type' => 'all',
            ]);

            if ($result['success'] ?? false) {
                Notification::make()
                    ->title(__('WordPress Updated'))
                    ->body(__('Core, plugins, and themes have been updated.'))
                    ->success()
                    ->send();
                $this->loadData();
                $this->resetTable();
            } else {
                throw new Exception($result['error'] ?? __('Update failed'));
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Update Failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function createStaging(string $siteId, string $subdomain, string $targetDomain = '', string $targetType = 'subdomain'): void
    {
        try {
            $sourceSite = collect($this->sites)->firstWhere('id', $siteId);
            $sourceDomain = (string) ($sourceSite['domain'] ?? '');
            $normalizedSourceDomain = strtolower(trim($sourceDomain));

            $agentPayload = [
                'username' => $this->getUsername(),
                'site_id' => $siteId,
            ];

            if ($targetType === 'domain') {
                $targetDomain = strtolower(trim($targetDomain));
                if ($targetDomain === '') {
                    throw new Exception(__('Please choose a target domain.'));
                }
                if ($targetDomain === $normalizedSourceDomain) {
                    throw new Exception(__('The staging domain must be different from the source domain.'));
                }
                if (! $this->isOwnedDomain($targetDomain)) {
                    throw new Exception(__('The selected domain is not in your domain list.'));
                }
                $agentPayload['target_domain'] = $targetDomain;
            } else {
                $subdomain = strtolower(trim($subdomain));
                if ($subdomain === '') {
                    throw new Exception(__('Please enter a subdomain.'));
                }
                if (! preg_match('/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/', $subdomain)) {
                    throw new Exception(__('Subdomain can contain only letters, numbers, and hyphens.'));
                }
                $agentPayload['subdomain'] = $subdomain;
            }

            Notification::make()
                ->title(__('Creating Staging Environment...'))
                ->body(__('This may take several minutes.'))
                ->info()
                ->send();

            $result = $this->getAgent()->send('wp.create_staging', $agentPayload);

            if ($result['success'] ?? false) {
                $stagingDomain = (string) ($result['staging_domain'] ?? '');
                if ($stagingDomain !== '') {
                    Domain::firstOrCreate(
                        [
                            'user_id' => Auth::id(),
                            'domain' => $stagingDomain,
                        ],
                        [
                            'document_root' => '/home/'.$this->getUsername().'/domains/'.$stagingDomain.'/public_html',
                            'is_active' => true,
                            'ssl_enabled' => false,
                            'directory_index' => 'index.php index.html',
                            'page_cache_enabled' => false,
                        ]
                    );
                }

                if (
                    $sourceDomain !== ''
                    && $stagingDomain !== ''
                    && str_ends_with(strtolower($stagingDomain), '.'.strtolower($sourceDomain))
                ) {
                    $this->ensureStagingDnsRecords($sourceDomain, $stagingDomain);
                }

                Notification::make()
                    ->title(__('Staging Environment Created'))
                    ->body(__('Your staging site is available at: :url', ['url' => $result['staging_url'] ?? '']))
                    ->success()
                    ->persistent()
                    ->send();
                $this->loadData();
                $this->resetTable();
            } else {
                throw new Exception($result['error'] ?? __('Failed to create staging environment'));
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Staging Creation Failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function pushStaging(string $stagingSiteId): void
    {
        try {
            Notification::make()
                ->title(__('Pushing staging to production...'))
                ->body(__('This may take several minutes.'))
                ->info()
                ->send();

            $result = $this->getAgent()->wpPushStaging($this->getUsername(), $stagingSiteId);

            if ($result['success'] ?? false) {
                Notification::make()
                    ->title(__('Staging pushed to production'))
                    ->success()
                    ->send();
                $this->loadData();
                $this->resetTable();
            } else {
                throw new Exception($result['error'] ?? __('Failed to push staging site'));
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Push Failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function flushCache(string $siteId): void
    {
        try {
            $result = $this->getAgent()->wpCacheFlush($this->getUsername(), $siteId);

            if ($result['success'] ?? false) {
                $keysDeleted = $result['keys_deleted'] ?? null;
                if ($keysDeleted !== null) {
                    $body = __('Object cache has been flushed. (:count keys deleted)', ['count' => $keysDeleted]);
                } else {
                    $body = __('Object cache has been flushed.');
                }

                Notification::make()
                    ->title(__('Cache Flushed'))
                    ->body($body)
                    ->success()
                    ->send();
            } else {
                throw new Exception($result['error'] ?? __('Failed to flush cache'));
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Flush Failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function getCacheStatus(string $siteId): array
    {
        try {
            $result = $this->getAgent()->wpCacheStatus($this->getUsername(), $siteId);
            if ($result['success'] ?? false) {
                return $result['status'];
            }
        } catch (Exception $e) {
            // Ignore errors, return empty status
        }

        return [
            'enabled' => false,
            'drop_in_installed' => false,
            'plugin_installed' => false,
            'redis_connected' => false,
            'cached_keys' => 0,
        ];
    }

    public function runSecurityScan(string $siteId): void
    {
        // Find the site
        $site = collect($this->sites)->firstWhere('id', $siteId);
        if (! $site) {
            Notification::make()
                ->title(__('Site not found'))
                ->danger()
                ->send();

            return;
        }

        // Check if WPScan is available (scan runs asynchronously via the queue).
        try {
            $status = $this->getAgent()->send('scanner.status', ['tool' => 'wpscan']);
        } catch (Exception $e) {
            $status = ['installed' => false, 'error' => $e->getMessage()];
        }

        if (! ($status['installed'] ?? false)) {
            Notification::make()
                ->title(__('WPScan not available'))
                ->body(__('Please contact your administrator to enable security scanning.'))
                ->warning()
                ->send();

            return;
        }

        $this->isSecurityScanning = true;
        $this->scanningSiteId = $siteId;
        $this->scanningSiteUrl = (string) ($site['url'] ?? '');

        if ($this->scanningSiteUrl === '' || filter_var($this->scanningSiteUrl, FILTER_VALIDATE_URL) === false) {
            $this->isSecurityScanning = false;
            $this->scanningSiteId = null;
            $this->scanningSiteUrl = null;

            Notification::make()
                ->title(__('Invalid site URL'))
                ->danger()
                ->send();

            return;
        }

        $this->securityScanResults = [
            'url' => $this->scanningSiteUrl,
            'scan_time' => now()->format('Y-m-d H:i:s'),
            'status' => 'running',
        ];

        Notification::make()
            ->title(__('Starting security scan...'))
            ->body(__('Scanning :url for vulnerabilities.', ['url' => $this->scanningSiteUrl]))
            ->info()
            ->send();

        $this->showSecurityScanModal = true;

        // Queue scan; results will be picked up by pollSecurityScan().
        dispatch(new RunWpscanScan($this->scanningSiteUrl));
    }

    public function pollSecurityScan(): void
    {
        if (! $this->isSecurityScanning || ! $this->scanningSiteUrl) {
            return;
        }

        $resultsPath = storage_path('app/security-scans/wpscan-latest.json');
        if (! is_file($resultsPath)) {
            return;
        }

        $raw = json_decode((string) @file_get_contents($resultsPath), true);
        if (! is_array($raw)) {
            $this->securityScanResults = [
                'error' => __('Failed to parse scan results'),
                'url' => $this->scanningSiteUrl,
                'scan_time' => now()->format('Y-m-d H:i:s'),
            ];
            $this->isSecurityScanning = false;
            return;
        }

        $rawUrl = (string) ($raw['url'] ?? $raw['target_url'] ?? '');
        if ($rawUrl !== '' && $rawUrl !== $this->scanningSiteUrl) {
            // Another scan completed; ignore.
            return;
        }

        $raw['url'] = $this->scanningSiteUrl;
        $raw['scan_time'] = $raw['scan_time'] ?? now()->format('Y-m-d H:i:s');

        $this->securityScanResults = $this->parseWpScanResults($raw);
        $this->isSecurityScanning = false;

        $vulnCount = count($this->securityScanResults['vulnerabilities'] ?? []);
        $notification = Notification::make()
            ->title(__('Security scan completed'))
            ->body($vulnCount > 0
                ? __('Found :count potential vulnerability(ies)', ['count' => $vulnCount])
                : __('No vulnerabilities found!'));

        ($vulnCount > 0 ? $notification->warning() : $notification->success())->send();
    }

    protected function parseWpScanResults(array $results): array
    {
        $parsed = [
            'url' => $results['url'] ?? ($results['target_url'] ?? ''),
            'scan_time' => $results['scan_time'] ?? now()->format('Y-m-d H:i:s'),
            'wordpress_version' => null,
            'main_theme' => null,
            'plugins' => [],
            'vulnerabilities' => [],
            'interesting_findings' => [],
        ];

        // WordPress version
        if (isset($results['version']['number'])) {
            $parsed['wordpress_version'] = [
                'number' => $results['version']['number'],
                'status' => $results['version']['status'] ?? __('unknown'),
                'vulnerabilities' => [],
            ];

            if (! empty($results['version']['vulnerabilities'])) {
                foreach ($results['version']['vulnerabilities'] as $vuln) {
                    $parsed['vulnerabilities'][] = [
                        'type' => __('WordPress Core'),
                        'title' => $vuln['title'] ?? __('Unknown vulnerability'),
                        'references' => $vuln['references'] ?? [],
                        'fixed_in' => $vuln['fixed_in'] ?? null,
                    ];
                }
            }
        }

        // Main theme
        if (isset($results['main_theme']['slug'])) {
            $parsed['main_theme'] = [
                'name' => $results['main_theme']['slug'],
                'version' => $results['main_theme']['version']['number'] ?? __('Unknown'),
            ];

            if (! empty($results['main_theme']['vulnerabilities'])) {
                foreach ($results['main_theme']['vulnerabilities'] as $vuln) {
                    $parsed['vulnerabilities'][] = [
                        'type' => __('Theme: :name', ['name' => $results['main_theme']['slug']]),
                        'title' => $vuln['title'] ?? __('Unknown vulnerability'),
                        'references' => $vuln['references'] ?? [],
                        'fixed_in' => $vuln['fixed_in'] ?? null,
                    ];
                }
            }
        }

        // Plugins
        if (! empty($results['plugins'])) {
            foreach ($results['plugins'] as $slug => $plugin) {
                $parsed['plugins'][] = [
                    'name' => $slug,
                    'version' => $plugin['version']['number'] ?? __('Unknown'),
                ];

                if (! empty($plugin['vulnerabilities'])) {
                    foreach ($plugin['vulnerabilities'] as $vuln) {
                        $parsed['vulnerabilities'][] = [
                            'type' => __('Plugin: :name', ['name' => $slug]),
                            'title' => $vuln['title'] ?? __('Unknown vulnerability'),
                            'references' => $vuln['references'] ?? [],
                            'fixed_in' => $vuln['fixed_in'] ?? null,
                        ];
                    }
                }
            }
        }

        // Interesting findings
        if (! empty($results['interesting_findings'])) {
            foreach ($results['interesting_findings'] as $finding) {
                $parsed['interesting_findings'][] = [
                    'type' => $finding['type'] ?? 'info',
                    'description' => $finding['to_s'] ?? ($finding['url'] ?? __('Unknown finding')),
                    'url' => $finding['url'] ?? null,
                ];
            }
        }

        return $parsed;
    }

    public function closeSecurityScanModal(): void
    {
        $this->showSecurityScanModal = false;
        $this->securityScanResults = [];
        $this->scanningSiteId = null;
        $this->scanningSiteUrl = null;
        $this->isSecurityScanning = false;
    }

    public function showCredentialsModal(): void
    {
        $this->mountAction('showCredentialsAction');
    }

    public function showCredentialsAction(): Action
    {
        return Action::make('showCredentialsAction')
            ->modalHeading(__('WordPress Installed!'))
            ->modalDescription(__('Save these credentials! They won\'t be shown again.'))
            ->modalIcon('heroicon-o-check-circle')
            ->modalIconColor('success')
            ->modalSubmitAction(false)
            ->modalCancelActionLabel(__('Done'))
            ->infolist([
                Section::make(__('Site Information'))
                    ->icon('heroicon-o-globe-alt')
                    ->columns(1)
                    ->schema([
                        TextEntry::make('site_url')
                            ->label(__('Site URL'))
                            ->state(fn () => $this->credentials['url'] ?? '')
                            ->copyable()
                            ->fontFamily('mono'),
                        TextEntry::make('admin_url')
                            ->label(__('Admin URL'))
                            ->state(fn () => $this->credentials['admin_url'] ?? '')
                            ->copyable()
                            ->fontFamily('mono'),
                    ]),
                Section::make(__('Admin Credentials'))
                    ->icon('heroicon-o-user')
                    ->columns(2)
                    ->schema([
                        TextEntry::make('admin_user')
                            ->label(__('Username'))
                            ->state(fn () => $this->credentials['admin_user'] ?? '')
                            ->copyable()
                            ->fontFamily('mono'),
                        TextEntry::make('admin_password')
                            ->label(__('Password'))
                            ->state(fn () => $this->credentials['admin_password'] ?? '')
                            ->copyable()
                            ->fontFamily('mono'),
                    ]),
                Section::make(__('Database Credentials'))
                    ->icon('heroicon-o-circle-stack')
                    ->columns(1)
                    ->schema([
                        TextEntry::make('db_name')
                            ->label(__('Database Name'))
                            ->state(fn () => $this->credentials['db_name'] ?? '')
                            ->copyable()
                            ->fontFamily('mono'),
                        TextEntry::make('db_user')
                            ->label(__('Database User'))
                            ->state(fn () => $this->credentials['db_user'] ?? '')
                            ->copyable()
                            ->fontFamily('mono'),
                        TextEntry::make('db_password')
                            ->label(__('Database Password'))
                            ->state(fn () => $this->credentials['db_password'] ?? '')
                            ->copyable()
                            ->fontFamily('mono'),
                    ]),
            ]);
    }

    public function showScanResultsModal(): void
    {
        $this->mountAction('showScanResultsAction');
    }

    public function showScanResultsAction(): Action
    {
        return Action::make('showScanResultsAction')
            ->modalHeading(__('Found WordPress Sites'))
            ->modalDescription(__('The following WordPress installations were found in your home folder and are not yet tracked. Click "Import" to add them to your dashboard.'))
            ->modalIcon('heroicon-o-magnifying-glass')
            ->modalIconColor('info')
            ->modalSubmitAction(false)
            ->modalCancelActionLabel(__('Close'))
            ->infolist([
                Section::make(__('Discovered Sites'))
                    ->schema(
                        collect($this->scannedSites)->map(fn ($site, $index) => TextEntry::make("site_{$index}")
                            ->label($site['site_url'] ?? __('WordPress Site'))
                            ->state($site['path'])
                            ->helperText(isset($site['version']) ? 'v'.$site['version'] : '')
                        )->toArray()
                    ),
            ]);
    }

    public function showSecurityScanResultsModal(): void
    {
        $this->mountAction('showSecurityScanResultsAction');
    }

    public function showSecurityScanResultsAction(): Action
    {
        $results = $this->securityScanResults;
        $vulnCount = count($results['vulnerabilities'] ?? []);

        return Action::make('showSecurityScanResultsAction')
            ->modalHeading(__('Security Scan Results'))
            ->modalDescription($results['url'] ?? '')
            ->modalIcon('heroicon-o-shield-check')
            ->modalIconColor($vulnCount > 0 ? 'danger' : 'success')
            ->modalSubmitAction(false)
            ->modalCancelActionLabel(__('Close'))
            ->infolist(function () use ($results, $vulnCount): array {
                $schema = [];

                // Scan info
                $schema[] = Section::make(__('Scan Information'))
                    ->icon('heroicon-o-information-circle')
                    ->columns(2)
                    ->schema([
                        TextEntry::make('scanned_url')
                            ->label(__('Scanned URL'))
                            ->state($results['url'] ?? __('Unknown')),
                        TextEntry::make('scan_time')
                            ->label(__('Scan Time'))
                            ->state($results['scan_time'] ?? ''),
                    ]);

                // WordPress version
                if (isset($results['wordpress_version'])) {
                    $schema[] = Section::make(__('WordPress Version'))
                        ->icon('heroicon-o-code-bracket')
                        ->schema([
                            TextEntry::make('wp_version')
                                ->label(__('Version'))
                                ->state($results['wordpress_version']['number'])
                                ->badge()
                                ->color(match ($results['wordpress_version']['status'] ?? '') {
                                    'insecure' => 'danger',
                                    'outdated' => 'warning',
                                    default => 'success',
                                }),
                        ]);
                }

                // Vulnerabilities
                if ($vulnCount > 0) {
                    $vulnEntries = [];
                    foreach ($results['vulnerabilities'] as $index => $vuln) {
                        $vulnEntries[] = TextEntry::make("vuln_{$index}")
                            ->label($vuln['type'])
                            ->state($vuln['title'])
                            ->helperText($vuln['fixed_in'] ? __('Fixed in: :version', ['version' => $vuln['fixed_in']]) : '')
                            ->color('danger');
                    }
                    $schema[] = Section::make(__('Vulnerabilities Found')." ({$vulnCount})")
                        ->icon('heroicon-o-exclamation-triangle')
                        ->iconColor('danger')
                        ->schema($vulnEntries);
                } else {
                    $schema[] = Section::make(__('No Vulnerabilities Found'))
                        ->icon('heroicon-o-check-circle')
                        ->iconColor('success')
                        ->description(__('Your WordPress site appears to be secure'));
                }

                // Interesting findings
                if (! empty($results['interesting_findings'])) {
                    $findingEntries = [];
                    foreach (array_slice($results['interesting_findings'], 0, 10) as $index => $finding) {
                        $findingEntries[] = TextEntry::make("finding_{$index}")
                            ->hiddenLabel()
                            ->state($finding['description']);
                    }
                    $schema[] = Section::make(__('Interesting Findings'))
                        ->icon('heroicon-o-eye')
                        ->collapsed()
                        ->schema($findingEntries);
                }

                // Detected plugins
                if (! empty($results['plugins'])) {
                    $pluginEntries = [];
                    foreach ($results['plugins'] as $index => $plugin) {
                        $pluginEntries[] = TextEntry::make("plugin_{$index}")
                            ->hiddenLabel()
                            ->state($plugin['name'].' v'.$plugin['version'])
                            ->badge()
                            ->color('gray');
                    }
                    $schema[] = Section::make(__('Detected Plugins').' ('.count($results['plugins']).')')
                        ->icon('heroicon-o-puzzle-piece')
                        ->collapsed()
                        ->schema($pluginEntries);
                }

                return $schema;
            });
    }

    public function captureScreenshot(string $siteId): void
    {
        $site = collect($this->sites)->firstWhere('id', $siteId);
        if (! $site) {
            Notification::make()
                ->title(__('Site not found'))
                ->danger()
                ->send();

            return;
        }

        $url = (string) ($site['url'] ?? '');

        try {
            if ($url === '' || filter_var($url, FILTER_VALIDATE_URL) === false) {
                Notification::make()
                    ->title(__('Screenshot failed'))
                    ->body(__('Invalid site URL.'))
                    ->warning()
                    ->send();

                return;
            }

            $result = $this->getAgent()->send('screenshot.capture', [
                'url' => $url,
                'site_id' => $siteId,
            ]);

            if ($result['success'] ?? false) {
                Notification::make()
                    ->title(__('Screenshot captured'))
                    ->body(__('Website screenshot has been updated.'))
                    ->success()
                    ->send();
            } else {
                Notification::make()
                    ->title(__('Screenshot failed'))
                    ->body($result['error'] ?? __('Could not capture screenshot.'))
                    ->warning()
                    ->send();
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Screenshot failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    public function getScreenshotUrl(string $siteId): ?string
    {
        $filename = 'wp_'.$siteId.'.png';
        $filepath = storage_path('app/public/screenshots/'.$filename);

        if (file_exists($filepath)) {
            // Add timestamp to bust cache
            return asset('storage/screenshots/'.$filename).'?t='.filemtime($filepath);
        }

        return null;
    }

    public function hasScreenshot(string $siteId): bool
    {
        $filename = 'wp_'.$siteId.'.png';

        return file_exists(storage_path('app/public/screenshots/'.$filename));
    }

    protected function ensureStagingDnsRecords(string $sourceDomainName, string $stagingDomainName): void
    {
        $sourceDomain = Domain::query()
            ->where('user_id', Auth::id())
            ->where('domain', $sourceDomainName)
            ->first();

        if (! $sourceDomain) {
            return;
        }

        $label = $this->extractSubdomainLabel($stagingDomainName, $sourceDomainName);
        if ($label === null || $label === '') {
            return;
        }

        $settings = DnsSetting::getAll();
        $defaultTtl = (int) ($settings['default_ttl'] ?? 3600);

        $defaultIpv4 = $sourceDomain->ip_address
            ?: ($settings['default_ip'] ?? ServerFacts::serverIp('127.0.0.1'));

        DnsRecord::query()->updateOrCreate(
            [
                'domain_id' => $sourceDomain->id,
                'name' => $label,
                'type' => 'A',
            ],
            [
                'content' => $defaultIpv4,
                'ttl' => $defaultTtl,
                'priority' => null,
            ]
        );

        $defaultIpv6 = $sourceDomain->ipv6_address ?: ($settings['default_ipv6'] ?? null);
        if (! empty($defaultIpv6)) {
            DnsRecord::query()->updateOrCreate(
                [
                    'domain_id' => $sourceDomain->id,
                    'name' => $label,
                    'type' => 'AAAA',
                ],
                [
                    'content' => $defaultIpv6,
                    'ttl' => $defaultTtl,
                    'priority' => null,
                ]
            );
        }

        try {
            $this->syncDnsZone($sourceDomain, $settings);
        } catch (Exception) {
            // Keep staging creation successful even if DNS sync is temporarily unavailable.
        }
    }

    protected function removeStagingDnsRecords(array $stagingSite): void
    {
        $stagingDomainName = strtolower(trim((string) ($stagingSite['domain'] ?? '')));
        if ($stagingDomainName === '') {
            return;
        }

        $sourceDomainName = '';
        $sourceSiteId = $stagingSite['source_site_id'] ?? null;
        if (is_string($sourceSiteId) && $sourceSiteId !== '') {
            $sourceSite = collect($this->sites)->firstWhere('id', $sourceSiteId);
            $sourceDomainName = strtolower(trim((string) ($sourceSite['domain'] ?? '')));
        }

        if ($sourceDomainName === '') {
            $parts = explode('.', $stagingDomainName, 2);
            if (count($parts) === 2) {
                $sourceDomainName = $parts[1];
            }
        }

        if ($sourceDomainName === '') {
            return;
        }

        $sourceDomain = Domain::query()
            ->where('user_id', Auth::id())
            ->where('domain', $sourceDomainName)
            ->first();

        if (! $sourceDomain) {
            return;
        }

        $label = $this->extractSubdomainLabel($stagingDomainName, $sourceDomainName);
        if ($label === null || $label === '') {
            return;
        }

        DnsRecord::query()
            ->where('domain_id', $sourceDomain->id)
            ->where('name', $label)
            ->whereIn('type', ['A', 'AAAA'])
            ->delete();

        try {
            $this->syncDnsZone($sourceDomain);
        } catch (Exception) {
            // Keep deletion successful even if DNS sync is temporarily unavailable.
        }
    }

    protected function syncDnsZone(Domain $domain, ?array $settings = null): void
    {
        $settings ??= DnsSetting::getAll();
        $records = DnsRecord::query()->where('domain_id', $domain->id)->get()->toArray();
        $hostname = gethostname() ?: 'localhost';
        $serverIp = ServerFacts::serverIp('');

        $this->getAgent()->dnsSyncZone($domain->domain, $records, [
            'ns1' => $settings['ns1'] ?? "ns1.{$hostname}",
            'ns2' => $settings['ns2'] ?? "ns2.{$hostname}",
            'admin_email' => $settings['admin_email'] ?? "admin.{$hostname}",
            'default_ip' => $settings['default_ip'] ?? ($serverIp !== '' ? $serverIp : '127.0.0.1'),
            'default_ipv6' => $settings['default_ipv6'] ?? null,
            'default_ttl' => $settings['default_ttl'] ?? 3600,
        ]);
    }

    protected function extractSubdomainLabel(string $fullDomain, string $baseDomain): ?string
    {
        $fullDomain = strtolower(trim($fullDomain, " \t\n\r\0\x0B."));
        $baseDomain = strtolower(trim($baseDomain, " \t\n\r\0\x0B."));

        if ($fullDomain === '' || $baseDomain === '') {
            return null;
        }

        if ($fullDomain === $baseDomain) {
            return null;
        }

        $suffix = '.'.$baseDomain;
        if (! str_ends_with($fullDomain, $suffix)) {
            return null;
        }

        $label = substr($fullDomain, 0, -strlen($suffix));

        return trim((string) $label, '.');
    }

    protected function getOwnedDomainOptions(array $exclude = []): array
    {
        $excludeSet = [];
        foreach ($exclude as $value) {
            $normalized = strtolower(trim((string) $value));
            if ($normalized !== '') {
                $excludeSet[$normalized] = true;
            }
        }

        $options = [];
        foreach ($this->domains as $domain) {
            $name = strtolower(trim((string) ($domain['domain'] ?? '')));
            if ($name === '' || isset($excludeSet[$name])) {
                continue;
            }
            $options[$name] = $name;
        }

        return $options;
    }

    protected function isOwnedDomain(string $domain): bool
    {
        $domain = strtolower(trim($domain));
        if ($domain === '') {
            return false;
        }

        foreach ($this->domains as $ownedDomain) {
            $candidate = strtolower(trim((string) ($ownedDomain['domain'] ?? '')));
            if ($candidate === $domain) {
                return true;
            }
        }

        return false;
    }
}
