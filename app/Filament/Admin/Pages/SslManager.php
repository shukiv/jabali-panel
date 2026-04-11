<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Filament\Admin\Widgets\PanelCertificateWidget;
use App\Filament\Admin\Widgets\SslStatsOverview;
use App\Models\Domain;
use App\Models\SslCertificate;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Services\SslManagementService;
use App\Support\SafeError;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Forms\Components\Select;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Grouping\Group;
use Filament\Tables\Table;
use Illuminate\Support\Facades\Artisan;

class SslManager extends Page implements HasTable
{
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-shield-check';

    protected static ?int $navigationSort = 8;

    public static function getNavigationLabel(): string
    {
        return __('SSL Manager');
    }

    public function getTitle(): string
    {
        return __('SSL Manager');
    }

    protected string $view = 'filament.admin.pages.ssl-manager';

    public bool $isRunning = false;

    public string $autoSslLog = '';

    public ?string $lastUpdated = null;

    protected function getHeaderWidgets(): array
    {
        return [
            PanelCertificateWidget::class,
            SslStatsOverview::class,
        ];
    }

    public function getHeaderWidgetsColumns(): int|array
    {
        return 6;
    }

    public function mount(): void
    {
        $this->lastUpdated = now()->format('H:i:s');
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(
                SslCertificate::query()
                    ->with(['domain.user'])
                    ->whereHas('domain')
            )
            ->defaultGroup('domain.user.username')
            ->groups([
                Group::make('domain.user.username')
                    ->label(__('User'))
                    ->collapsible(),
            ])
            ->columns([
                TextColumn::make('domain.domain')
                    ->label(__('Hostname'))
                    ->formatStateUsing(fn (SslCertificate $record): string => $record->service === 'mail'
                        ? 'mail.'.$record->domain->domain
                        : $record->domain->domain
                    )
                    ->searchable(),
                TextColumn::make('service')
                    ->label(__('Service'))
                    ->badge()
                    ->formatStateUsing(fn (SslCertificate $record): string => $record->service === 'web' ? __('HTTPS') : __('Mail'))
                    ->color(fn (SslCertificate $record): string => $record->service === 'web' ? 'info' : 'warning'),
                TextColumn::make('type')
                    ->label(__('Type'))
                    ->badge()
                    ->color('gray')
                    ->formatStateUsing(fn (?string $state): string => $state ? ucfirst(str_replace('_', ' ', $state)) : __('None')),
                TextColumn::make('status')
                    ->label(__('Status'))
                    ->badge()
                    ->color(fn (?string $state): string => match ($state) {
                        'active' => 'success',
                        'expired', 'failed' => 'danger',
                        default => 'gray',
                    })
                    ->formatStateUsing(fn (?string $state): string => ucfirst($state ?? 'unknown')),
                TextColumn::make('expires_at')
                    ->label(__('Expires'))
                    ->date('M d, Y')
                    ->description(fn (SslCertificate $record): ?string => $record->expires_at
                        ? $record->days_until_expiry.'d'
                        : null
                    )
                    ->color(fn (SslCertificate $record): string => match (true) {
                        $record->days_until_expiry <= 7 => 'danger',
                        $record->days_until_expiry <= 30 => 'warning',
                        default => 'gray',
                    }),
            ])
            ->actions([
                \Filament\Actions\Action::make('renew')
                    ->icon('heroicon-o-arrow-path')
                    ->color('primary')
                    ->iconButton()
                    ->tooltip(__('Renew'))
                    ->visible(fn (SslCertificate $record): bool => $record->type === 'lets_encrypt' && $record->status === 'active')
                    ->action(fn (SslCertificate $record) => $this->renewSslForDomain($record->domain_id, $record->service)),
                \Filament\Actions\Action::make('issue')
                    ->icon('heroicon-o-check-circle')
                    ->color('success')
                    ->iconButton()
                    ->tooltip(__('Issue'))
                    ->visible(fn (SslCertificate $record): bool => in_array($record->type, ['self_signed', 'none', '', null], true) || in_array($record->status, ['pending', 'failed'], true))
                    ->action(fn (SslCertificate $record) => $record->service === 'mail'
                        ? $this->issueMailSslForDomain($record->domain_id)
                        : $this->issueSslForDomain($record->domain_id)
                    ),
                \Filament\Actions\Action::make('check')
                    ->icon('heroicon-o-magnifying-glass')
                    ->color('gray')
                    ->iconButton()
                    ->tooltip(__('Check'))
                    ->action(fn (SslCertificate $record) => $this->checkSslForDomain($record->domain_id)),
            ])
            ->emptyStateHeading(__('No SSL certificates found'))
            ->emptyStateDescription(__('Run SSL Check to scan your domains.'))
            ->emptyStateIcon('heroicon-o-shield-exclamation')
            ->paginated(false);
    }

    public function issueSslForDomain(int $domainId): void
    {
        try {
            $domain = Domain::with('user')->findOrFail($domainId);

            $service = app(SslManagementService::class);
            $cert = $service->issue($domain);

            if ($cert->status === 'failed') {
                Notification::make()
                    ->title(__('SSL Certificate Failed'))
                    ->body($cert->last_error ?? __('Unknown error'))
                    ->danger()
                    ->send();
            } else {
                $domain->update(['ssl_enabled' => true]);

                Notification::make()
                    ->title(__('SSL Certificate Issued'))
                    ->body(__('Certificate issued for :domain', ['domain' => $domain->domain]))
                    ->success()
                    ->send();
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }

        $this->lastUpdated = now()->format('H:i:s');
    }

    public function renewSslForDomain(int $domainId, string $service = 'web'): void
    {
        try {
            $domain = Domain::with('user')->findOrFail($domainId);

            $sslService = app(SslManagementService::class);
            $cert = $sslService->renew($domain, $service);

            if ($cert->status === 'failed') {
                Notification::make()
                    ->title(__('Renewal Failed'))
                    ->body($cert->last_error ?? __('Unknown error'))
                    ->danger()
                    ->send();
            } else {
                Notification::make()
                    ->title(__('Certificate Renewed'))
                    ->body(__('SSL certificate renewed for :domain', ['domain' => $domain->domain]))
                    ->success()
                    ->send();
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }

        $this->lastUpdated = now()->format('H:i:s');
    }

    public function checkSslForDomain(int $domainId): void
    {
        try {
            $domain = Domain::with('user')->findOrFail($domainId);

            $service = app(SslManagementService::class);
            $cert = $service->check($domain);

            if ($cert->status === 'active') {
                $domain->update(['ssl_enabled' => true]);

                Notification::make()
                    ->title(__('Certificate Checked'))
                    ->body(__('Found: :issuer', ['issuer' => $cert->issuer ?? __('Unknown')]))
                    ->success()
                    ->send();
            } else {
                Notification::make()
                    ->title(__('Certificate Checked'))
                    ->body(__('No certificate found'))
                    ->success()
                    ->send();
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }

        $this->lastUpdated = now()->format('H:i:s');
    }

    public function issueMailSslForDomain(int $domainId): void
    {
        try {
            $domain = Domain::with('user')->findOrFail($domainId);
            $mailHostname = 'mail.'.$domain->domain;

            $service = app(SslManagementService::class);
            $cert = $service->issue($domain, 'mail');

            if ($cert->status === 'failed') {
                Notification::make()
                    ->title(__('Mail SSL Certificate Failed'))
                    ->body($cert->last_error ?? __('Unknown error'))
                    ->danger()
                    ->send();
            } else {
                Notification::make()
                    ->title(__('Mail SSL Certificate Issued'))
                    ->body(__('Certificate issued for :hostname', ['hostname' => $mailHostname]))
                    ->success()
                    ->send();
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }

        $this->lastUpdated = now()->format('H:i:s');
    }

    public function renewMailSslForDomain(int $domainId): void
    {
        $this->issueMailSslForDomain($domainId);
    }

    public function runAutoSsl(?string $domain = null): void
    {
        $this->isRunning = true;
        $this->autoSslLog = '';

        try {
            $logDir = storage_path('logs/ssl');
            if (! is_dir($logDir)) {
                @mkdir($logDir, 0775, true);
            }

            $params = [];
            if ($domain) {
                $params['--domain'] = $domain;
            }

            Artisan::call('jabali:ssl-check', $params);
            $this->autoSslLog = Artisan::output();

            Notification::make()
                ->title(__('SSL Check Complete'))
                ->body($domain
                    ? __('SSL check completed for :domain', ['domain' => $domain])
                    : __('SSL certificate check completed for all domains'))
                ->success()
                ->send();
        } catch (Exception $e) {
            $this->autoSslLog = __('Error: :message', ['message' => $e->getMessage()]);

            Notification::make()
                ->title(__('SSL Check Failed'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }

        $this->isRunning = false;
        $this->lastUpdated = now()->format('H:i:s');
    }

    public function runSslCheckForUser(int $userId): void
    {
        $this->isRunning = true;
        $this->autoSslLog = '';

        try {
            $user = User::findOrFail($userId);
            $domains = Domain::where('user_id', $userId)->pluck('domain')->toArray();

            if (empty($domains)) {
                $this->autoSslLog = __('No domains found for user :user', ['user' => $user->username]);
                Notification::make()
                    ->title(__('No Domains'))
                    ->body(__('User :user has no domains', ['user' => $user->username]))
                    ->warning()
                    ->send();
                $this->isRunning = false;

                return;
            }

            $this->autoSslLog = __('Checking SSL for :count domains of user :user', ['count' => count($domains), 'user' => $user->username])."\n\n";

            foreach ($domains as $domain) {
                Artisan::call('jabali:ssl-check', ['--domain' => $domain]);
                $this->autoSslLog .= Artisan::output()."\n";
            }

            Notification::make()
                ->title(__('SSL Check Complete'))
                ->body(__('SSL check completed for :count domains of user :user', ['count' => count($domains), 'user' => $user->username]))
                ->success()
                ->send();
        } catch (Exception $e) {
            $this->autoSslLog = __('Error: :message', ['message' => $e->getMessage()]);

            Notification::make()
                ->title(__('SSL Check Failed'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }

        $this->isRunning = false;
        $this->lastUpdated = now()->format('H:i:s');
    }

    public function issueAllPending(): void
    {
        $domainsWithoutSsl = Domain::whereDoesntHave('sslCertificate')
            ->orWhereHas('sslCertificate', function ($q) {
                $q->where('status', 'failed');
            })
            ->with('user')
            ->get();

        $issued = 0;
        $failed = 0;
        $service = app(SslManagementService::class);

        foreach ($domainsWithoutSsl as $domain) {
            try {
                $cert = $service->issue($domain);

                if ($cert->status === 'failed') {
                    $failed++;
                } else {
                    $domain->update(['ssl_enabled' => true]);
                    $issued++;
                }
            } catch (Exception $e) {
                $failed++;
            }
        }

        $domainsWithoutMailSsl = Domain::whereDoesntHave('mailSslCertificate')
            ->with('user')
            ->get();

        foreach ($domainsWithoutMailSsl as $domain) {
            try {
                $cert = $service->issue($domain, 'mail');

                if ($cert->status === 'failed') {
                    $failed++;
                } else {
                    $issued++;
                }
            } catch (Exception $e) {
                $failed++;
            }
        }

        Notification::make()
            ->title(__('Bulk SSL Issuance Complete'))
            ->body(__('Issued: :issued, Failed: :failed', ['issued' => $issued, 'failed' => $failed]))
            ->success()
            ->send();

        $this->lastUpdated = now()->format('H:i:s');
    }

    public function getLetsEncryptLog(): string
    {
        try {
            $agent = app(AgentClient::class);
            $result = $agent->send('ssl.certbot_log', ['lines' => 500]);

            if ($result['success'] ?? false) {
                return "=== {$result['file']} ===\n".$result['log'];
            }

            return $result['error'] ?? __('No certbot log files found.');
        } catch (Exception $e) {
            return __('Failed to read certbot log: ').$e->getMessage();
        }
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('runAutoSsl')
                ->label(__('Run SSL Check'))
                ->icon('heroicon-o-play')
                ->color('success')
                ->modalHeading(__('Run SSL Check'))
                ->modalDescription(__('Check SSL certificates and automatically issue/renew them.'))
                ->modalWidth('md')
                ->form([
                    Select::make('scope')
                        ->label(__('Scope'))
                        ->options([
                            'all' => __('All Domains'),
                            'user' => __('Specific User'),
                            'domain' => __('Specific Domain'),
                        ])
                        ->default('all')
                        ->live()
                        ->required(),
                    Select::make('user_id')
                        ->label(__('User'))
                        ->options(fn () => User::pluck('username', 'id')->toArray())
                        ->searchable()
                        ->visible(fn ($get) => $get('scope') === 'user')
                        ->required(fn ($get) => $get('scope') === 'user'),
                    Select::make('domain')
                        ->label(__('Domain'))
                        ->options(fn () => Domain::pluck('domain', 'domain')->toArray())
                        ->searchable()
                        ->visible(fn ($get) => $get('scope') === 'domain')
                        ->required(fn ($get) => $get('scope') === 'domain'),
                ])
                ->action(function (array $data): void {
                    match ($data['scope']) {
                        'user' => $this->runSslCheckForUser((int) $data['user_id']),
                        'domain' => $this->runAutoSsl($data['domain']),
                        default => $this->runAutoSsl(),
                    };
                }),
            Action::make('issueAllPending')
                ->label(__('Issue All Pending'))
                ->icon('heroicon-o-shield-check')
                ->color('primary')
                ->requiresConfirmation()
                ->modalHeading(__('Issue SSL for All Pending Domains'))
                ->modalDescription(__('This will attempt to issue SSL certificates for all domains without active certificates. This may take a while.'))
                ->action(fn () => $this->issueAllPending()),
            Action::make('viewLog')
                ->label(__('View Log'))
                ->icon('heroicon-o-document-text')
                ->color('gray')
                ->modalHeading(__("Let's Encrypt Log"))
                ->modalWidth('4xl')
                ->modalContent(fn () => view('filament.admin.pages.ssl-log-modal', ['log' => $this->getLetsEncryptLog()]))
                ->modalSubmitAction(false)
                ->modalCancelActionLabel(__('Close')),
        ];
    }
}
