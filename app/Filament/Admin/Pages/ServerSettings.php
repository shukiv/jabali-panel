<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Filament\Admin\Widgets\Settings\DatabaseTuningTable;
use App\Filament\Admin\Widgets\Settings\DnssecTable;
use App\Filament\Admin\Widgets\Settings\NotificationLogTable;
use App\Models\DnsSetting;
use App\Models\HostingPackage;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Support\ServerFacts;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Action as FormAction;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\FileUpload;
use Filament\Forms\Components\Placeholder;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Actions;
use Filament\Schemas\Components\EmbeddedTable;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\Tabs\Tab;
use Filament\Schemas\Schema;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\File;
use Illuminate\Support\Facades\Mail;
use Illuminate\Support\Facades\Response;
use Illuminate\Support\Facades\Storage;
use Livewire\Attributes\Url;
use Livewire\WithFileUploads;

class ServerSettings extends Page implements HasActions, HasForms
{
    use InteractsWithActions;
    use InteractsWithForms;
    use WithFileUploads;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-cog-6-tooth';

    protected static ?int $navigationSort = 3;

    protected static ?string $slug = 'server-settings';

    public static function getNavigationLabel(): string
    {
        return __('Server Settings');
    }

    protected string $view = 'filament.admin.pages.server-settings';

    // Form data arrays
    public ?array $brandingData = [];

    public ?array $hostnameData = [];

    public ?array $dnsData = [];

    public ?array $resolversData = [];

    public ?array $quotaData = [];

    public ?array $fileManagerData = [];

    public ?array $emailData = [];

    public ?array $notificationsData = [];

    public ?array $phpFpmData = [];

    // Version info (non-form)
    public bool $isSystemdResolved = false;

    public ?string $currentLogo = null;

    #[Url(as: 'tab')]
    public ?string $activeTab = 'general';

    public function getLogoUrlProperty(): ?string
    {
        if ($this->currentLogo && Storage::disk('public')->exists($this->currentLogo)) {
            return asset('storage/'.$this->currentLogo);
        }

        return null;
    }

    protected function getAgent(): AgentClient
    {
        return new AgentClient;
    }

    public function getTitle(): string|Htmlable
    {
        return __('Server Settings');
    }

    protected function normalizeTabName(?string $tab): string
    {
        return match ($tab) {
            'general', 'dns', 'storage', 'email', 'notifications', 'php-fpm', 'database' => $tab,
            default => 'general',
        };
    }

    public function setTab(string $tab): void
    {
        $this->activeTab = $this->normalizeTabName($tab);
    }

    public function updatedActiveTab(): void
    {
        $this->activeTab = $this->normalizeTabName($this->activeTab);
    }

    public function mount(): void
    {
        $this->activeTab = $this->normalizeTabName($this->activeTab);
        $settings = DnsSetting::getAll();
        $hostname = gethostname() ?: 'localhost';
        $serverIp = ServerFacts::serverIp('');

        $this->currentLogo = $settings['custom_logo'] ?? null;
        $this->isSystemdResolved = ServerFacts::isSystemdResolved();

        // Load hostname from agent
        $agentHostname = $hostname;
        try {
            $result = $this->getAgent()->send('server.info', []);
            if ($result['success'] ?? false) {
                $agentHostname = $result['info']['hostname'] ?? $hostname;
            }
        } catch (Exception $e) {
            // Use default
        }

        // Load resolvers
        $resolvers = ['', '', ''];
        $searchDomain = '';
        if (file_exists('/etc/resolv.conf')) {
            $content = file_get_contents('/etc/resolv.conf');
            $lines = explode("\n", $content);
            $ns = [];
            foreach ($lines as $line) {
                $line = trim($line);
                if (str_starts_with($line, 'nameserver ')) {
                    $ns[] = trim(substr($line, 11));
                } elseif (str_starts_with($line, 'search ')) {
                    $searchDomain = trim(substr($line, 7));
                }
            }
            $resolvers = [$ns[0] ?? '', $ns[1] ?? '', $ns[2] ?? ''];
        }
        if (trim($searchDomain) === 'example.com') {
            $searchDomain = '';
        }

        // Fill form data
        $this->brandingData = [
            'panel_name' => $settings['panel_name'] ?? 'Jabali',
        ];

        $this->hostnameData = [
            'hostname' => $agentHostname,
        ];

        $this->dnsData = [
            'ns1' => $settings['ns1'] ?? "ns1.{$hostname}",
            'ns1_ip' => $settings['ns1_ip'] ?? $serverIp,
            'ns2' => $settings['ns2'] ?? "ns2.{$hostname}",
            'ns2_ip' => $settings['ns2_ip'] ?? $serverIp,
            'default_ip' => $settings['default_ip'] ?? $serverIp,
            'default_ipv6' => $settings['default_ipv6'] ?? '',
            'default_ttl' => $settings['default_ttl'] ?? '3600',
            'admin_email' => $settings['admin_email'] ?? "admin.{$hostname}",
        ];

        $this->resolversData = [
            'resolver1' => $resolvers[0],
            'resolver2' => $resolvers[1],
            'resolver3' => $resolvers[2],
            'search_domain' => $searchDomain,
        ];

        $this->quotaData = [
            'quotas_enabled' => (bool) ($settings['quotas_enabled'] ?? false),
            'default_quota_mb' => (int) ($settings['default_quota_mb'] ?? 5120),
        ];

        $this->fileManagerData = [
            'max_upload_size_mb' => (int) ($settings['max_upload_size_mb'] ?? 100),
        ];

        $this->emailData = [
            'mail_hostname' => $settings['mail_hostname'] ?? "mail.{$hostname}",
            'mail_default_quota_mb' => (int) ($settings['mail_default_quota_mb'] ?? 1024),
            'max_mailboxes_per_domain' => (int) ($settings['max_mailboxes_per_domain'] ?? 10),
            'webmail_url' => $settings['webmail_url'] ?? '/webmail',
            'webmail_product_name' => $settings['webmail_product_name'] ?? 'Jabali Webmail',
        ];

        $this->notificationsData = [
            'admin_email_recipients' => $settings['admin_email_recipients'] ?? '',
            'notify_ssl_errors' => (bool) ($settings['notify_ssl_errors'] ?? true),
            'notify_backup_failures' => (bool) ($settings['notify_backup_failures'] ?? true),
            'notify_backup_success' => (bool) ($settings['notify_backup_success'] ?? false),
            'notify_disk_quota' => (bool) ($settings['notify_disk_quota'] ?? true),
            'notify_login_failures' => (bool) ($settings['notify_login_failures'] ?? true),
            'notify_ssh_logins' => (bool) ($settings['notify_ssh_logins'] ?? false),
            'notify_system_updates' => (bool) ($settings['notify_system_updates'] ?? false),
            'notify_service_health' => (bool) ($settings['notify_service_health'] ?? true),
            'notify_high_load' => (bool) ($settings['notify_high_load'] ?? true),
            'load_threshold' => (float) ($settings['load_threshold'] ?? 5.0),
            'load_alert_minutes' => (int) ($settings['load_alert_minutes'] ?? 5),
        ];

        $this->phpFpmData = [
            'pm_max_children' => (int) ($settings['fpm_pm_max_children'] ?? 5),
            'pm_max_requests' => (int) ($settings['fpm_pm_max_requests'] ?? 200),
            'rlimit_files' => (int) ($settings['fpm_rlimit_files'] ?? 1024),
            'process_priority' => (int) ($settings['fpm_process_priority'] ?? 0),
            'request_terminate_timeout' => (int) ($settings['fpm_request_terminate_timeout'] ?? 300),
            'memory_limit' => $settings['fpm_memory_limit'] ?? '512M',
        ];

    }

    public function settingsForm(Schema $schema): Schema
    {
        return $schema
            ->schema([
                Tabs::make(__('Server Settings Sections'))
                    ->contained()
                    ->livewireProperty('activeTab')
                    ->tabs([
                        'general' => Tab::make(__('General'))
                            ->icon('heroicon-o-cog-6-tooth')
                            ->schema($this->generalTabContent()),
                        'dns' => Tab::make(__('DNS'))
                            ->icon('heroicon-o-globe-alt')
                            ->schema($this->dnsTabContent()),
                        'storage' => Tab::make(__('Storage'))
                            ->icon('heroicon-o-circle-stack')
                            ->schema($this->storageTabContent()),
                        'email' => Tab::make(__('Email'))
                            ->icon('heroicon-o-envelope')
                            ->schema($this->emailTabContent()),
                        'notifications' => Tab::make(__('Notifications'))
                            ->icon('heroicon-o-bell')
                            ->schema($this->notificationsTabContent()),
                        'php-fpm' => Tab::make(__('PHP-FPM'))
                            ->icon('heroicon-o-cpu-chip')
                            ->schema($this->phpFpmTabContent()),
                        'database' => Tab::make(__('Database Tuning'))
                            ->icon('heroicon-o-circle-stack')
                            ->schema($this->databaseTabContent()),
                    ]),
            ]);
    }

    protected function generalTabContent(): array
    {
        return [
            Section::make(__('Panel Branding'))
                ->icon('heroicon-o-paint-brush')
                ->schema([
                    Grid::make(['default' => 1, 'md' => 2])->schema([
                        TextInput::make('brandingData.panel_name')
                            ->label(__('Control Panel Name'))
                            ->placeholder(__('Jabali'))
                            ->helperText(__('Appears in browser title and navigation'))
                            ->required(),
                    ]),
                    Actions::make([
                        FormAction::make('uploadLogo')
                            ->label(__('Upload Logo'))
                            ->icon('heroicon-o-arrow-up-tray')
                            ->color('gray')
                            ->form([
                                FileUpload::make('logo')
                                    ->label(__('Logo Image'))
                                    ->image()
                                    ->disk('public')
                                    ->directory('branding')
                                    ->visibility('public')
                                    ->acceptedFileTypes(['image/png', 'image/svg+xml', 'image/jpeg', 'image/webp'])
                                    ->maxSize(1024)
                                    ->required()
                                    ->helperText(__('SVG, PNG, JPEG or WebP, max 1MB')),
                            ])
                            ->action(function (array $data): void {
                                $this->uploadLogo($data);
                            }),
                        FormAction::make('removeLogo')
                            ->label(__('Remove Logo'))
                            ->color('danger')
                            ->icon('heroicon-o-trash')
                            ->requiresConfirmation()
                            ->action(fn () => $this->removeLogo())
                            ->visible(fn () => $this->currentLogo !== null),
                        FormAction::make('saveBranding')
                            ->label(__('Save Branding'))
                            ->action('saveBranding'),
                    ]),
                ]),
            Section::make(__('Server Hostname'))
                ->icon('heroicon-o-server')
                ->schema([
                    TextInput::make('hostnameData.hostname')
                        ->label(__('Hostname'))
                        ->placeholder(__('server.example.com'))
                        ->required(),
                    Actions::make([
                        FormAction::make('saveHostname')
                            ->label(__('Save Hostname'))
                            ->action('saveHostname'),
                    ]),
                ]),
        ];
    }

    protected function dnsTabContent(): array
    {
        return [
            Section::make(__('Nameservers'))
                ->icon('heroicon-o-server-stack')
                ->schema([
                    Grid::make(['default' => 1, 'md' => 2, 'lg' => 4])->schema([
                        TextInput::make('dnsData.ns1')->label(__('NS1 Hostname'))->placeholder(__('ns1.example.com')),
                        TextInput::make('dnsData.ns1_ip')->label(__('NS1 IP Address'))->placeholder(__('192.168.1.1')),
                        TextInput::make('dnsData.ns2')->label(__('NS2 Hostname'))->placeholder(__('ns2.example.com')),
                        TextInput::make('dnsData.ns2_ip')->label(__('NS2 IP Address'))->placeholder(__('192.168.1.2')),
                    ]),
                ]),
            Section::make(__('Zone Defaults'))
                ->schema([
                    Grid::make(['default' => 1, 'md' => 3])->schema([
                        TextInput::make('dnsData.default_ip')
                            ->label(__('Default Server IP'))
                            ->placeholder(__('192.168.1.1'))
                            ->helperText(__('Default A record IP for new zones')),
                        TextInput::make('dnsData.default_ipv6')
                            ->label(__('Default IPv6'))
                            ->placeholder(__('2001:db8::1'))
                            ->helperText(__('Default AAAA record IP for new zones'))
                            ->rule('nullable|ipv6'),
                        TextInput::make('dnsData.default_ttl')
                            ->label(__('Default TTL'))
                            ->placeholder(__('3600')),
                    ]),
                    TextInput::make('dnsData.admin_email')
                        ->label(__('Admin Email (SOA)'))
                        ->placeholder(__('admin.example.com'))
                        ->helperText(__('Use dots instead of @ (e.g., admin.example.com)')),
                    Actions::make([
                        FormAction::make('saveDns')
                            ->label(__('Save DNS Settings'))
                            ->action('saveDns'),
                    ]),
                ]),
            Section::make(__('DNS Resolvers'))
                ->description($this->isSystemdResolved ? __('systemd-resolved active') : null)
                ->icon('heroicon-o-signal')
                ->schema([
                    Actions::make([
                        FormAction::make('applyCloudflareResolvers')
                            ->label(__('Use Cloudflare'))
                            ->action('applyCloudflareResolvers'),
                        FormAction::make('applyGoogleResolvers')
                            ->label(__('Use Google'))
                            ->action('applyGoogleResolvers'),
                        FormAction::make('applyQuad9Resolvers')
                            ->label(__('Use Quad9'))
                            ->action('applyQuad9Resolvers'),
                    ])->alignment('left'),
                    Grid::make(['default' => 1, 'md' => 2, 'lg' => 4])->schema([
                        TextInput::make('resolversData.resolver1')->label(__('Resolver 1'))->placeholder(__('8.8.8.8')),
                        TextInput::make('resolversData.resolver2')->label(__('Resolver 2'))->placeholder(__('8.8.4.4')),
                        TextInput::make('resolversData.resolver3')->label(__('Resolver 3'))->placeholder(__('1.1.1.1')),
                        TextInput::make('resolversData.search_domain')->label(__('Search Domain'))->placeholder(__('example.com')),
                    ]),
                    Actions::make([
                        FormAction::make('saveResolvers')
                            ->label(__('Save Resolvers'))
                            ->action('saveResolvers'),
                    ]),
                ]),
            Section::make(__('DNSSEC'))
                ->description(__('DNS Security Extensions'))
                ->icon('heroicon-o-shield-check')
                ->schema([
                    EmbeddedTable::make(DnssecTable::class),
                ]),
        ];
    }

    public function applyCloudflareResolvers(): void
    {
        $this->applyResolverTemplate('cloudflare');
    }

    public function applyGoogleResolvers(): void
    {
        $this->applyResolverTemplate('google');
    }

    public function applyQuad9Resolvers(): void
    {
        $this->applyResolverTemplate('quad9');
    }

    public function applyResolverTemplate(string $template): void
    {
        $templates = [
            'cloudflare' => ['1.1.1.1', '1.0.0.1', '2606:4700:4700::1111'],
            'google' => ['8.8.8.8', '8.8.4.4', '2001:4860:4860::8888'],
            'quad9' => ['9.9.9.9', '149.112.112.112', '2620:fe::fe'],
        ];

        $resolvers = $templates[$template] ?? null;

        if (! $resolvers) {
            return;
        }

        $current = $this->resolversData ?? [];
        $this->resolversData = array_merge($current, [
            'resolver1' => $resolvers[0] ?? '',
            'resolver2' => $resolvers[1] ?? '',
            'resolver3' => $resolvers[2] ?? '',
        ]);

        $form = $this->getForm('settingsForm');
        if ($form) {
            $state = $form->getState();
            $state['resolversData'] = $this->resolversData;
            $form->fill($state);
        }

        $this->dispatch('$refresh');

        Notification::make()
            ->title(__('DNS resolver template applied'))
            ->success()
            ->send();
    }

    protected function storageTabContent(): array
    {
        return [
            Section::make(__('Disk Quotas'))
                ->icon('heroicon-o-chart-pie')
                ->schema([
                    Placeholder::make('zfs_notice')
                        ->content(__('ZFS filesystem detected. Linux disk quotas are not available. ZFS manages quotas natively via: zfs set userquota@<user>=<size> <dataset>'))
                        ->visible(fn () => self::isZfsFilesystem()),
                    Grid::make(['default' => 1, 'md' => 2])
                        ->schema([
                            Toggle::make('quotaData.quotas_enabled')
                                ->label(__('Enable Disk Quotas'))
                                ->helperText(__('When enabled, disk usage limits will be enforced for user accounts'))
                                ->disabled(fn () => self::isZfsFilesystem()),
                            TextInput::make('quotaData.default_quota_mb')
                                ->label(__('Default Quota (MB)'))
                                ->numeric()
                                ->placeholder(__('5120'))
                                ->helperText(__('Default disk quota for new users (5120 MB = 5 GB)'))
                                ->disabled(fn () => self::isZfsFilesystem()),
                        ])
                        ->hidden(fn () => self::isZfsFilesystem()),
                    Actions::make([
                        FormAction::make('saveQuotaSettings')
                            ->label(__('Save Quota Settings'))
                            ->action('saveQuotaSettings'),
                    ])->hidden(fn () => self::isZfsFilesystem()),
                ]),
            Section::make(__('File Manager'))
                ->icon('heroicon-o-folder')
                ->schema([
                    TextInput::make('fileManagerData.max_upload_size_mb')
                        ->label(__('Max Upload Size (MB)'))
                        ->numeric()
                        ->minValue(1)
                        ->maxValue(500)
                        ->placeholder(__('100'))
                        ->helperText(__('Maximum file size users can upload (1-500 MB)')),
                    Actions::make([
                        FormAction::make('saveFileManagerSettings')
                            ->label(__('Save'))
                            ->action('saveFileManagerSettings'),
                    ]),
                ]),
        ];
    }

    protected function emailTabContent(): array
    {
        return [
            Section::make(__('Mail Server'))
                ->icon('heroicon-o-envelope')
                ->schema([
                    Grid::make(['default' => 1, 'md' => 2])->schema([
                        TextInput::make('emailData.mail_hostname')
                            ->label(__('Mail Server Hostname'))
                            ->placeholder(__('mail.example.com'))
                            ->helperText(__('The hostname used for mail server identification')),
                        TextInput::make('emailData.mail_default_quota_mb')
                            ->label(__('Default Mailbox Quota (MB)'))
                            ->numeric()
                            ->minValue(100)
                            ->maxValue(10240),
                        TextInput::make('emailData.max_mailboxes_per_domain')
                            ->label(__('Max Mailboxes Per Domain'))
                            ->numeric()
                            ->minValue(1)
                            ->maxValue(1000),
                    ]),
                ]),
            Section::make(__('Webmail'))
                ->icon('heroicon-o-globe-alt')
                ->schema([
                    Grid::make(['default' => 1, 'md' => 2])->schema([
                        TextInput::make('emailData.webmail_url')
                            ->label(__('Webmail URL'))
                            ->placeholder(__('/webmail'))
                            ->helperText(__('URL path for Roundcube webmail')),
                        TextInput::make('emailData.webmail_product_name')
                            ->label(__('Webmail Product Name'))
                            ->placeholder(__('Jabali Webmail'))
                            ->helperText(__('Name displayed on the webmail login page')),
                    ]),
                    Actions::make([
                        FormAction::make('openWebmail')
                            ->label(__('Open Webmail'))
                            ->icon('heroicon-o-arrow-top-right-on-square')
                            ->color('gray')
                            ->url('/webmail', shouldOpenInNewTab: true),
                        FormAction::make('saveEmailSettings')
                            ->label(__('Save Email Settings'))
                            ->action('saveEmailSettings'),
                    ]),
                ]),
        ];
    }

    protected function notificationsTabContent(): array
    {
        return [
            Section::make(__('Admin Recipients'))
                ->icon('heroicon-o-user-group')
                ->schema([
                    TextInput::make('notificationsData.admin_email_recipients')
                        ->label(__('Email Addresses'))
                        ->placeholder(__('admin@example.com, alerts@example.com'))
                        ->helperText(__('Comma-separated list of email addresses to receive notifications')),
                ]),
            Section::make(__('Notification Types & High Load Alerts'))
                ->icon('heroicon-o-bell-alert')
                ->schema([
                    Grid::make(['default' => 1, 'md' => 2])->schema([
                        Toggle::make('notificationsData.notify_ssl_errors')
                            ->label(__('SSL Certificate Alerts'))
                            ->helperText(__('Errors and expiring certificates')),
                        Toggle::make('notificationsData.notify_backup_failures')
                            ->label(__('Backup Failures'))
                            ->helperText(__('Failed scheduled backups')),
                        Toggle::make('notificationsData.notify_backup_success')
                            ->label(__('Backup Success'))
                            ->helperText(__('Successful backup completions')),
                        Toggle::make('notificationsData.notify_disk_quota')
                            ->label(__('Disk Quota Warnings'))
                            ->helperText(__('When users reach 90% quota')),
                        Toggle::make('notificationsData.notify_login_failures')
                            ->label(__('Login Failure Alerts'))
                            ->helperText(__('Brute force and Fail2ban alerts')),
                        Toggle::make('notificationsData.notify_ssh_logins')
                            ->label(__('SSH Login Alerts'))
                            ->helperText(__('Successful SSH login notifications')),
                        Toggle::make('notificationsData.notify_system_updates')
                            ->label(__('System Updates Available'))
                            ->helperText(__('When panel updates are available')),
                        Toggle::make('notificationsData.notify_service_health')
                            ->label(__('Service Health Alerts'))
                            ->helperText(__('Service failures and auto-restarts')),
                    ]),
                    Grid::make(['default' => 1, 'md' => 3])->schema([
                        Toggle::make('notificationsData.notify_high_load')
                            ->label(__('Enable High Load Alerts'))
                            ->helperText(__('Alert when server load is high')),
                        TextInput::make('notificationsData.load_threshold')
                            ->label(__('Load Threshold'))
                            ->numeric()
                            ->minValue(1)
                            ->maxValue(100)
                            ->step(0.5)
                            ->placeholder(__('5'))
                            ->helperText(__('Alert when load exceeds this value')),
                        TextInput::make('notificationsData.load_alert_minutes')
                            ->label(__('Alert After (minutes)'))
                            ->numeric()
                            ->minValue(1)
                            ->maxValue(60)
                            ->placeholder(__('5'))
                            ->helperText(__('Minutes of high load before alerting')),
                    ]),
                    Actions::make([
                        FormAction::make('sendTestEmail')
                            ->label(__('Send Test Email'))
                            ->color('gray')
                            ->action('sendTestEmail'),
                        FormAction::make('saveEmailNotificationSettings')
                            ->label(__('Save Notification Settings'))
                            ->action('saveEmailNotificationSettings'),
                    ]),
                ]),
            Section::make(__('Notification Log'))
                ->description(__('Last 30 days'))
                ->icon('heroicon-o-document-text')
                ->schema([
                    EmbeddedTable::make(NotificationLogTable::class),
                ]),
        ];
    }

    protected function phpFpmTabContent(): array
    {
        return [
            Section::make(__('Default Pool Limits'))
                ->description(__('These settings apply to new user pools. Use "Apply to All" to update existing pools.'))
                ->icon('heroicon-o-adjustments-horizontal')
                ->schema([
                    Grid::make(['default' => 1, 'md' => 2, 'lg' => 3])->schema([
                        TextInput::make('phpFpmData.pm_max_children')
                            ->label(__('Max Processes'))
                            ->numeric()
                            ->minValue(1)
                            ->maxValue(50)
                            ->helperText(__('Max PHP workers per user (1-50)')),
                        TextInput::make('phpFpmData.pm_max_requests')
                            ->label(__('Max Requests'))
                            ->numeric()
                            ->minValue(50)
                            ->maxValue(10000)
                            ->helperText(__('Requests before worker recycle')),
                        TextInput::make('phpFpmData.memory_limit')
                            ->label(__('Memory Limit'))
                            ->placeholder(__('512M'))
                            ->helperText(__('PHP memory_limit (e.g., 512M, 1G)')),
                    ]),
                    Grid::make(['default' => 1, 'md' => 2, 'lg' => 3])->schema([
                        TextInput::make('phpFpmData.rlimit_files')
                            ->label(__('Open Files Limit'))
                            ->numeric()
                            ->minValue(256)
                            ->maxValue(65536)
                            ->helperText(__('Max open file descriptors')),
                        TextInput::make('phpFpmData.process_priority')
                            ->label(__('Process Priority'))
                            ->numeric()
                            ->minValue(-20)
                            ->maxValue(19)
                            ->helperText(__('Nice value (-20 to 19, lower = higher priority)')),
                        TextInput::make('phpFpmData.request_terminate_timeout')
                            ->label(__('Request Timeout (s)'))
                            ->numeric()
                            ->minValue(30)
                            ->maxValue(3600)
                            ->helperText(__('Kill slow requests after this time')),
                    ]),
                    Actions::make([
                        FormAction::make('saveFpmSettings')
                            ->label(__('Save Settings'))
                            ->action('saveFpmSettings'),
                        FormAction::make('applyFpmToAll')
                            ->label(__('Apply to All Users'))
                            ->color('warning')
                            ->icon('heroicon-o-arrow-path')
                            ->requiresConfirmation()
                            ->modalHeading(__('Apply FPM Settings to All Users'))
                            ->modalDescription(__('This will update all existing PHP-FPM pool configurations with the current settings. PHP-FPM will be reloaded.'))
                            ->action('applyFpmToAll'),
                    ]),
                ]),
        ];
    }

    protected function databaseTabContent(): array
    {
        return [
            Section::make(__('Warning: Changing database settings can impact performance or cause outages'))
                ->description(__('Apply changes only if you understand their effects, and prefer doing so during maintenance windows.'))
                ->icon('heroicon-o-exclamation-triangle')
                ->iconColor('warning')
                ->collapsed(false)
                ->collapsible(false)
                ->compact(),
            Section::make(__('Database Tuning'))
                ->description(__('Adjust MariaDB/MySQL global variables.'))
                ->icon('heroicon-o-circle-stack')
                ->schema([
                    EmbeddedTable::make(DatabaseTuningTable::class),
                ]),
        ];
    }

    protected function getForms(): array
    {
        return [
            'settingsForm',
        ];
    }

    public function saveBranding(): void
    {
        $data = $this->brandingData;

        if (empty(trim($data['panel_name'] ?? ''))) {
            Notification::make()->title(__('Panel name cannot be empty'))->danger()->send();

            return;
        }

        DnsSetting::set('panel_name', trim($data['panel_name']));
        DnsSetting::clearCache();

        Notification::make()->title(__('Branding updated'))->body(__('Refresh to see changes.'))->success()->send();
    }

    public function uploadLogo(array $data): void
    {
        try {
            $logo = $data['logo'] ?? null;
            if (empty($logo)) {
                Notification::make()->title(__('No file selected'))->warning()->send();

                return;
            }

            // Filament FileUpload returns an array of stored file paths
            $path = is_array($logo) ? ($logo[0] ?? null) : $logo;

            if ($path) {
                // Delete old logo if exists
                if ($this->currentLogo && Storage::disk('public')->exists($this->currentLogo)) {
                    Storage::disk('public')->delete($this->currentLogo);
                }

                DnsSetting::set('custom_logo', $path);
                DnsSetting::clearCache();
                $this->currentLogo = $path;

                Notification::make()->title(__('Logo uploaded'))->body(__('Refresh to see changes.'))->success()->send();
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to upload logo'))->body($e->getMessage())->danger()->send();
        }
    }

    public function removeLogo(): void
    {
        try {
            if ($this->currentLogo && Storage::disk('public')->exists($this->currentLogo)) {
                Storage::disk('public')->delete($this->currentLogo);
            }
            DnsSetting::set('custom_logo', null);
            DnsSetting::clearCache();
            $this->currentLogo = null;
            Notification::make()->title(__('Logo removed'))->success()->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to remove logo'))->body($e->getMessage())->danger()->send();
        }
    }

    public function saveHostname(): void
    {
        $hostname = $this->hostnameData['hostname'] ?? '';

        if (empty(trim($hostname))) {
            Notification::make()->title(__('Hostname cannot be empty'))->danger()->send();

            return;
        }

        $result = $this->getAgent()->send('server.set_hostname', ['hostname' => $hostname]);

        if (! ($result['success'] ?? false)) {
            Notification::make()->title(__('Failed to update hostname'))->body($result['error'] ?? __('Unknown error'))->danger()->send();

            return;
        }

        // Restart or reload affected services
        $services = ['postfix', 'dovecot', 'nginx', 'named'];
        $updatedServices = [];
        $failedServices = [];

        foreach ($services as $service) {
            try {
                $action = $service === 'nginx' ? 'reload' : 'restart';
                $result = $this->getAgent()->send("service.{$action}", ['service' => $service]);
                if ($result['success'] ?? false) {
                    $updatedServices[] = $service;
                } else {
                    $failedServices[] = $service;
                }
            } catch (Exception $e) {
                $failedServices[] = $service;
            }
        }

        if (empty($failedServices)) {
            Notification::make()
                ->title(__('Hostname updated'))
                ->body(__('Affected services have been restarted or reloaded. PHP-FPM reload is skipped to avoid interrupting active sessions.'))
                ->success()
                ->send();
        } else {
            Notification::make()
                ->title(__('Hostname updated'))
                ->body(__('Some services failed to restart or reload: :services. PHP-FPM reload is skipped to avoid interrupting active sessions.', ['services' => implode(', ', $failedServices)]))
                ->warning()
                ->send();
        }
    }

    public function saveDns(): void
    {
        $data = $this->dnsData;

        DnsSetting::set('ns1', $data['ns1']);
        DnsSetting::set('ns1_ip', $data['ns1_ip']);
        DnsSetting::set('ns2', $data['ns2']);
        DnsSetting::set('ns2_ip', $data['ns2_ip']);
        DnsSetting::set('default_ip', $data['default_ip']);
        DnsSetting::set('default_ipv6', $data['default_ipv6'] ?: null);
        DnsSetting::set('default_ttl', $data['default_ttl']);
        DnsSetting::set('admin_email', $data['admin_email']);
        DnsSetting::clearCache();

        $result = $this->getAgent()->send('server.create_zone', [
            'hostname' => $this->hostnameData['hostname'],
            'ns1' => $data['ns1'],
            'ns1_ip' => $data['ns1_ip'],
            'ns2' => $data['ns2'],
            'ns2_ip' => $data['ns2_ip'],
            'admin_email' => $data['admin_email'],
            'server_ip' => $data['default_ip'],
            'server_ipv6' => $data['default_ipv6'],
            'ttl' => $data['default_ttl'],
        ]);

        if ($result['success'] ?? false) {
            Notification::make()->title(__('DNS settings saved'))->success()->send();
        } else {
            Notification::make()->title(__('Settings saved but zone creation failed'))->body($result['error'] ?? __('Unknown error'))->warning()->send();
        }
    }

    public function saveResolvers(): void
    {
        $data = $this->resolversData;

        try {
            $nameservers = array_filter([
                $data['resolver1'],
                $data['resolver2'],
                $data['resolver3'],
            ], fn ($ns) => ! empty(trim($ns ?? '')));

            if (empty($nameservers)) {
                Notification::make()->title(__('Failed to update DNS resolvers'))->body(__('At least one nameserver is required'))->danger()->send();

                return;
            }

            $result = $this->getAgent()->send('server.set_resolvers', [
                'nameservers' => array_values($nameservers),
                'search_domains' => ! empty($data['search_domain']) ? [$data['search_domain']] : [],
            ]);

            if ($result['success'] ?? false) {
                Notification::make()->title(__('DNS resolvers updated'))->success()->send();
            } else {
                Notification::make()->title(__('Failed to update DNS resolvers'))->body($result['error'] ?? __('Unknown error'))->danger()->send();
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Failed to update DNS resolvers'))->body($e->getMessage())->danger()->send();
        }
    }

    protected static function isZfsFilesystem(): bool
    {
        static $result = null;
        if ($result === null) {
            $fsHome = trim(shell_exec('findmnt -n -o FSTYPE /home 2>/dev/null') ?? '');
            $fsRoot = trim(shell_exec('findmnt -n -o FSTYPE / 2>/dev/null') ?? '');
            $result = $fsHome === 'zfs' || $fsRoot === 'zfs';
        }

        return $result;
    }

    public function saveQuotaSettings(): void
    {
        $data = $this->quotaData;
        $wasEnabled = (bool) DnsSetting::get('quotas_enabled', false);

        DnsSetting::set('quotas_enabled', $data['quotas_enabled'] ? '1' : '0');
        DnsSetting::set('default_quota_mb', (string) $data['default_quota_mb']);
        DnsSetting::clearCache();

        if ($data['quotas_enabled'] && ! $wasEnabled) {
            try {
                $result = $this->getAgent()->send('quota.enable', ['mount' => '/home']);
                if ($result['success'] ?? false) {
                    Notification::make()->title(__('Disk quotas enabled'))->body(__('Quota system has been initialized on /home'))->success()->send();
                } else {
                    Notification::make()->title(__('Settings saved'))->body(__('Warning: Could not enable quota system on filesystem.'))->warning()->send();
                }
            } catch (Exception $e) {
                Notification::make()->title(__('Settings saved'))->body(__('Warning: Could not enable quota system.'))->warning()->send();
            }
        }

        Notification::make()->title(__('Quota settings saved'))->success()->send();
    }

    public function saveFileManagerSettings(): void
    {
        $data = $this->fileManagerData;
        $size = max(1, min(500, (int) $data['max_upload_size_mb']));

        DnsSetting::set('max_upload_size_mb', (string) $size);
        DnsSetting::clearCache();

        try {
            $result = $this->getAgent()->send('server.set_upload_limits', ['size_mb' => $size]);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('File manager settings saved'))->body(__('Server upload limits updated to :size MB', ['size' => $size]))->success()->send();
            } else {
                Notification::make()->title(__('Settings saved'))->body(__('Database updated but server config update had issues'))->warning()->send();
            }
        } catch (Exception $e) {
            Notification::make()->title(__('Settings saved'))->body(__('Database updated but could not update server configs'))->warning()->send();
        }
    }

    public function saveEmailSettings(): void
    {
        $data = $this->emailData;

        DnsSetting::set('mail_hostname', $data['mail_hostname']);
        DnsSetting::set('mail_default_quota_mb', (string) $data['mail_default_quota_mb']);
        DnsSetting::set('max_mailboxes_per_domain', (string) $data['max_mailboxes_per_domain']);
        DnsSetting::set('webmail_url', $data['webmail_url']);
        DnsSetting::set('webmail_product_name', $data['webmail_product_name']);
        DnsSetting::clearCache();

        // Update Roundcube config
        $configFile = '/etc/roundcube/config.inc.php';
        if (file_exists($configFile)) {
            try {
                $content = file_get_contents($configFile);
                $content = preg_replace(
                    "/\\\$config\['product_name'\]\s*=\s*'[^']*';/",
                    "\$config['product_name'] = '".addslashes($data['webmail_product_name'])."';",
                    $content
                );
                file_put_contents($configFile, $content);
            } catch (Exception $e) {
                // Silently fail
            }
        }

        Notification::make()->title(__('Email settings saved'))->success()->send();
    }

    public function saveEmailNotificationSettings(): void
    {
        $data = $this->notificationsData;
        $emails = $this->parseNotificationRecipients($data['admin_email_recipients'] ?? '', false);
        if ($emails === null) {
            return;
        }

        $emailsValue = $emails === [] ? '' : implode(', ', $emails);
        $this->notificationsData['admin_email_recipients'] = $emailsValue;
        DnsSetting::set('admin_email_recipients', $emailsValue);
        DnsSetting::set('notify_ssl_errors', $data['notify_ssl_errors'] ? '1' : '0');
        DnsSetting::set('notify_backup_failures', $data['notify_backup_failures'] ? '1' : '0');
        DnsSetting::set('notify_backup_success', $data['notify_backup_success'] ? '1' : '0');
        DnsSetting::set('notify_disk_quota', $data['notify_disk_quota'] ? '1' : '0');
        DnsSetting::set('notify_login_failures', $data['notify_login_failures'] ? '1' : '0');
        DnsSetting::set('notify_ssh_logins', $data['notify_ssh_logins'] ? '1' : '0');
        DnsSetting::set('notify_system_updates', $data['notify_system_updates'] ? '1' : '0');
        DnsSetting::set('notify_service_health', $data['notify_service_health'] ? '1' : '0');
        DnsSetting::set('notify_high_load', $data['notify_high_load'] ? '1' : '0');
        DnsSetting::set('load_threshold', (string) max(1, min(100, (float) ($data['load_threshold'] ?? 5))));
        DnsSetting::set('load_alert_minutes', (string) max(1, min(60, (int) ($data['load_alert_minutes'] ?? 5))));
        DnsSetting::clearCache();

        Notification::make()->title(__('Notification settings saved'))->success()->send();
    }

    public function sendTestEmail(): void
    {
        $recipients = $this->notificationsData['admin_email_recipients'] ?? '';
        $recipientList = $this->parseNotificationRecipients($recipients, true);
        if ($recipientList === null) {
            return;
        }

        try {
            $hostname = gethostname() ?: 'localhost';
            $sender = "webmaster@{$hostname}";
            $subject = __('Test Email');
            $message = __('This is a test email from your Jabali Panel at :hostname.', ['hostname' => $hostname]).
                "\n\n".__('If you received this email, your admin notifications are working correctly.');

            Mail::raw(
                $message,
                function ($mail) use ($recipientList, $sender, $subject) {
                    $mail->from($sender, 'Jabali Panel');
                    $mail->to($recipientList);
                    $mail->subject('[Jabali] '.$subject);
                }
            );

            // Log the test email
            \App\Models\NotificationLog::log(
                'test',
                $subject,
                $message,
                $recipientList,
                'sent'
            );

            Notification::make()->title(__('Test email sent'))->body(__('Check your inbox for the test email'))->success()->send();
        } catch (Exception $e) {
            // Log the failed test email
            \App\Models\NotificationLog::log(
                'test',
                __('Test Email'),
                __('This is a test email from your Jabali Panel.'),
                array_map('trim', explode(',', $recipients)),
                'failed',
                null,
                $e->getMessage()
            );

            Notification::make()->title(__('Failed to send test email'))->body($e->getMessage())->danger()->send();
        }
    }

    /**
     * @return array<int, string>|null
     */
    protected function parseNotificationRecipients(?string $recipients, bool $requireOne): ?array
    {
        $value = trim((string) $recipients);
        if ($value === '') {
            if ($requireOne) {
                $this->addError('notificationsData.admin_email_recipients', __('Please add at least one email address.'));
                Notification::make()->title(__('Email Addresses required'))->body(__('Please add at least one email address in Email Addresses.'))->warning()->send();

                return null;
            }

            return [];
        }

        if (str_contains($value, ';')) {
            $this->addError('notificationsData.admin_email_recipients', __('Use a comma-separated list of email addresses.'));
            Notification::make()->title(__('Invalid recipient format'))->body(__('Use commas to separate email addresses.'))->danger()->send();

            return null;
        }

        $emails = array_values(array_filter(array_map('trim', explode(',', $value)), fn (string $email): bool => $email !== ''));
        if ($requireOne && $emails === []) {
            $this->addError('notificationsData.admin_email_recipients', __('Please add at least one email address.'));
            Notification::make()->title(__('Email Addresses required'))->body(__('Please add at least one email address in Email Addresses.'))->warning()->send();

            return null;
        }

        foreach ($emails as $email) {
            if (! filter_var($email, FILTER_VALIDATE_EMAIL)) {
                $this->addError('notificationsData.admin_email_recipients', __('Use a comma-separated list of valid email addresses.'));
                Notification::make()->title(__('Invalid recipient email'))->body(__(':email is not a valid email address', ['email' => $email]))->danger()->send();

                return null;
            }
        }

        return $emails;
    }

    public function saveFpmSettings(): void
    {
        $data = $this->phpFpmData;

        DnsSetting::set('fpm_pm_max_children', (string) $data['pm_max_children']);
        DnsSetting::set('fpm_pm_max_requests', (string) $data['pm_max_requests']);
        DnsSetting::set('fpm_rlimit_files', (string) $data['rlimit_files']);
        DnsSetting::set('fpm_process_priority', (string) $data['process_priority']);
        DnsSetting::set('fpm_request_terminate_timeout', (string) $data['request_terminate_timeout']);
        DnsSetting::set('fpm_memory_limit', $data['memory_limit']);
        DnsSetting::clearCache();

        Notification::make()
            ->title(__('PHP-FPM settings saved'))
            ->body(__('New user pools will use these settings. Use "Apply to All" to update existing pools.'))
            ->success()
            ->send();
    }

    public function applyFpmToAll(): void
    {
        $data = $this->phpFpmData;

        try {
            $result = $this->getAgent()->send('php.update_all_pool_limits', [
                'pm_max_children' => (int) $data['pm_max_children'],
                'pm_max_requests' => (int) $data['pm_max_requests'],
                'rlimit_files' => (int) $data['rlimit_files'],
                'process_priority' => (int) $data['process_priority'],
                'request_terminate_timeout' => (int) $data['request_terminate_timeout'],
                'memory_limit' => $data['memory_limit'],
            ]);

            if ($result['success'] ?? false) {
                $updated = $result['updated'] ?? [];
                $errors = $result['errors'] ?? [];

                if (empty($errors)) {
                    Notification::make()
                        ->title(__('FPM pools updated'))
                        ->body(__(':count user pools updated. PHP-FPM will reload.', ['count' => count($updated)]))
                        ->success()
                        ->send();
                } else {
                    Notification::make()
                        ->title(__('Partial update'))
                        ->body(__(':success pools updated, :errors failed', [
                            'success' => count($updated),
                            'errors' => count($errors),
                        ]))
                        ->warning()
                        ->send();
                }
            } else {
                Notification::make()
                    ->title(__('Failed to update pools'))
                    ->body($result['error'] ?? __('Unknown error'))
                    ->danger()
                    ->send();
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Failed to update pools'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('export_config')
                ->label(__('Export'))
                ->icon('heroicon-o-arrow-down-tray')
                ->color('gray')
                ->action(fn () => $this->exportConfig()),
            Action::make('import_config')
                ->label(__('Import'))
                ->icon('heroicon-o-arrow-up-tray')
                ->color('gray')
                ->modalHeading(__('Import Configuration'))
                ->modalDescription(__('Upload a previously exported configuration file. This will overwrite your current settings.'))
                ->modalIcon('heroicon-o-arrow-up-tray')
                ->modalIconColor('warning')
                ->modalSubmitActionLabel(__('Import'))
                ->form([
                    FileUpload::make('config_file')
                        ->label(__('Configuration File'))
                        ->acceptedFileTypes(['application/json'])
                        ->required()
                        ->maxSize(1024)
                        ->helperText(__('Select a .json file exported from Jabali Panel')),
                ])
                ->action(fn (array $data) => $this->importConfig($data)),
        ];
    }

    public function exportConfig(): \Symfony\Component\HttpFoundation\StreamedResponse
    {
        $exportData = $this->buildExportPayload();

        $filename = 'jabali-config-'.date('Y-m-d-His').'.json';
        $content = json_encode($exportData, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES);

        Notification::make()->title(__('Configuration exported'))->success()->send();

        return Response::streamDownload(function () use ($content) {
            echo $content;
        }, $filename, ['Content-Type' => 'application/json']);
    }

    public function importConfig(array $data): void
    {
        try {
            if (empty($data['config_file'])) {
                throw new Exception(__('No file uploaded'));
            }

            $filePath = Storage::disk('local')->path($data['config_file']);

            if (! file_exists($filePath)) {
                throw new Exception(__('Uploaded file not found'));
            }

            $content = file_get_contents($filePath);
            $importData = json_decode($content, true);

            if (json_last_error() !== JSON_ERROR_NONE) {
                throw new Exception(__('Invalid JSON file: :error', ['error' => json_last_error_msg()]));
            }

            if (! isset($importData['settings']) || ! is_array($importData['settings'])) {
                throw new Exception(__('Invalid configuration file format'));
            }

            $importedSettings = 0;
            foreach ($importData['settings'] as $key => $value) {
                if (in_array($key, ['custom_logo'])) {
                    continue;
                }
                DnsSetting::set($key, $value);
                $importedSettings++;
            }

            $importedPackages = 0;
            if (isset($importData['hosting_packages']) && is_array($importData['hosting_packages'])) {
                $importedPackages = $this->importHostingPackages($importData['hosting_packages']);
            }

            $tokenSummary = ['imported' => 0, 'skipped' => 0];
            if (isset($importData['api_tokens']) && is_array($importData['api_tokens'])) {
                $tokenSummary = $this->importApiTokens($importData['api_tokens']);
            }

            $tuningSummary = ['applied' => 0, 'failed' => 0];
            if (isset($importData['database_tuning']) && is_array($importData['database_tuning'])) {
                $tuningSummary = $this->applyDatabaseTuningFromImport($importData['database_tuning']);
            }

            DnsSetting::clearCache();
            Storage::disk('local')->delete($data['config_file']);
            $this->mount();

            $message = __(':settings settings imported, :packages packages updated, :tokens tokens imported, :tuning tuning entries applied', [
                'settings' => $importedSettings,
                'packages' => $importedPackages,
                'tokens' => $tokenSummary['imported'],
                'tuning' => $tuningSummary['applied'],
            ]);
            Notification::make()->title(__('Configuration imported'))->body($message)->success()->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Import failed'))->body($e->getMessage())->danger()->send();
        }
    }

    /**
     * @return array<string, mixed>
     */
    public function buildExportPayload(): array
    {
        $settings = DnsSetting::getAll();
        unset($settings['custom_logo']);

        return [
            'version' => '1.1',
            'exported_at' => now()->toIso8601String(),
            'hostname' => gethostname(),
            'settings' => $settings,
            'hosting_packages' => HostingPackage::query()
                ->orderBy('name')
                ->get()
                ->map(fn (HostingPackage $package): array => [
                    'name' => $package->name,
                    'description' => $package->description,
                    'disk_quota_mb' => $package->disk_quota_mb,
                    'bandwidth_gb' => $package->bandwidth_gb,
                    'domains_limit' => $package->domains_limit,
                    'databases_limit' => $package->databases_limit,
                    'mailboxes_limit' => $package->mailboxes_limit,
                    'is_active' => $package->is_active,
                ])
                ->toArray(),
            'api_tokens' => $this->exportApiTokens(),
            'database_tuning' => $this->getDatabaseTuningSettings(),
        ];
    }

    /**
     * @return array<int, array<string, mixed>>
     */
    protected function exportApiTokens(): array
    {
        $adminUsers = User::query()
            ->where('is_admin', true)
            ->get(['id', 'email', 'username']);

        $tokens = [];
        foreach ($adminUsers as $admin) {
            foreach ($admin->tokens as $token) {
                $tokens[] = [
                    'name' => $token->name,
                    'token' => $token->token,
                    'abilities' => $token->abilities ?? [],
                    'last_used_at' => $token->last_used_at?->toIso8601String(),
                    'expires_at' => $token->expires_at?->toIso8601String(),
                    'created_at' => $token->created_at?->toIso8601String(),
                    'owner_email' => $admin->email,
                    'owner_username' => $admin->username,
                ];
            }
        }

        return $tokens;
    }

    /**
     * @return array<string, string>
     */
    protected function getDatabaseTuningSettings(): array
    {
        $paths = [
            '/etc/mysql/mariadb.conf.d/90-jabali-tuning.cnf',
            '/etc/mysql/conf.d/90-jabali-tuning.cnf',
        ];

        foreach ($paths as $path) {
            if (! file_exists($path)) {
                continue;
            }

            $lines = file($path, FILE_IGNORE_NEW_LINES) ?: [];
            $settings = [];
            foreach ($lines as $line) {
                $line = trim($line);
                if ($line === '' || str_starts_with($line, '#') || str_starts_with($line, '[')) {
                    continue;
                }

                if (! str_contains($line, '=')) {
                    continue;
                }

                [$key, $value] = array_map('trim', explode('=', $line, 2));
                if ($key !== '') {
                    $settings[$key] = $value;
                }
            }

            return $settings;
        }

        return [];
    }

    /**
     * @param  array<int, array<string, mixed>>  $packages
     */
    protected function importHostingPackages(array $packages): int
    {
        $imported = 0;
        foreach ($packages as $package) {
            if (! is_array($package) || empty($package['name'])) {
                continue;
            }

            HostingPackage::updateOrCreate(
                ['name' => $package['name']],
                [
                    'description' => $package['description'] ?? '',
                    'disk_quota_mb' => $package['disk_quota_mb'] ?? null,
                    'bandwidth_gb' => $package['bandwidth_gb'] ?? null,
                    'domains_limit' => $package['domains_limit'] ?? null,
                    'databases_limit' => $package['databases_limit'] ?? null,
                    'mailboxes_limit' => $package['mailboxes_limit'] ?? null,
                    'is_active' => (bool) ($package['is_active'] ?? true),
                ]
            );
            $imported++;
        }

        return $imported;
    }

    /**
     * @param  array<int, array<string, mixed>>  $tokens
     * @return array{imported:int,skipped:int}
     */
    protected function importApiTokens(array $tokens): array
    {
        $imported = 0;
        $skipped = 0;

        foreach ($tokens as $token) {
            if (! is_array($token) || empty($token['token'])) {
                $skipped++;

                continue;
            }

            $ownerEmail = $token['owner_email'] ?? null;
            $ownerUsername = $token['owner_username'] ?? null;
            $owner = User::query()
                ->where('is_admin', true)
                ->when($ownerEmail, fn ($query) => $query->where('email', $ownerEmail))
                ->when(! $ownerEmail && $ownerUsername, fn ($query) => $query->where('username', $ownerUsername))
                ->first();

            if (! $owner) {
                $skipped++;

                continue;
            }

            $tokenHash = $token['token'];
            $exists = DB::table('personal_access_tokens')->where('token', $tokenHash)->exists();
            if ($exists) {
                $skipped++;

                continue;
            }

            DB::table('personal_access_tokens')->insert([
                'tokenable_type' => User::class,
                'tokenable_id' => $owner->id,
                'name' => $token['name'] ?? 'API Token',
                'token' => $tokenHash,
                'abilities' => json_encode($token['abilities'] ?? []),
                'last_used_at' => $token['last_used_at'] ?? null,
                'expires_at' => $token['expires_at'] ?? null,
                'created_at' => $token['created_at'] ?? now()->toIso8601String(),
                'updated_at' => now()->toIso8601String(),
            ]);
            $imported++;
        }

        return ['imported' => $imported, 'skipped' => $skipped];
    }

    /**
     * @param  array<string, string>  $tuning
     * @return array{applied:int,failed:int}
     */
    protected function applyDatabaseTuningFromImport(array $tuning): array
    {
        $applied = 0;
        $failed = 0;

        foreach ($tuning as $name => $value) {
            if (! is_string($name) || $name === '') {
                $failed++;

                continue;
            }

            try {
                $agent = new AgentClient;
                $result = $agent->databasePersistTuning($name, (string) $value);
                if ($result['success'] ?? false) {
                    $applied++;
                } else {
                    $failed++;
                }
            } catch (Exception $e) {
                $failed++;
            }
        }

        return ['applied' => $applied, 'failed' => $failed];
    }
}
