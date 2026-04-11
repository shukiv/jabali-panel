<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Filament\Concerns\ManagesNginxDirectives;
use App\Models\AuditLog;
use App\Models\Domain;
use App\Models\DomainAlias;
use App\Models\DomainHotlinkSetting;
use App\Models\DomainRedirect;
use App\Services\Agent\InteractsWithAgent;
use App\Support\SafeError;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\ActionGroup;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Radio;
use Filament\Forms\Components\Repeater;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Grid;
use Filament\Support\Enums\Width;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Auth;

class Domains extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithAgent;
    use InteractsWithForms;
    use InteractsWithTable;
    use ManagesNginxDirectives;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-globe-alt';

    public static function getNavigationLabel(): string
    {
        return __('Domains');
    }

    protected static ?int $navigationSort = 2;

    protected string $view = 'filament.jabali.pages.domains';

    public function getTitle(): string|Htmlable
    {
        return __('Domains');
    }

    public function getUsername(): string
    {
        return Auth::user()->username;
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(Domain::query()->where('user_id', Auth::id()))
            ->columns([
                TextColumn::make('domain')
                    ->label(__('Domain'))
                    ->icon('heroicon-o-globe-alt')
                    ->iconColor('primary')
                    ->description(fn (Domain $record) => preg_replace('#^/home/[^/]+/#', '', $record->document_root ?? ''))
                    ->url(fn (Domain $record) => 'http://'.$record->domain, shouldOpenInNewTab: true)
                    ->searchable()
                    ->sortable(),
                IconColumn::make('is_active')
                    ->label(__('Status'))
                    ->boolean()
                    ->trueIcon('heroicon-o-check-circle')
                    ->falseIcon('heroicon-o-x-circle')
                    ->trueColor('success')
                    ->falseColor('danger'),
                IconColumn::make('ssl_enabled')
                    ->label(__('SSL'))
                    ->boolean()
                    ->trueIcon('heroicon-m-lock-closed')
                    ->falseIcon('heroicon-m-lock-open')
                    ->trueColor('success')
                    ->falseColor('warning'),
                IconColumn::make('page_cache_enabled')
                    ->label(__('Page Cache'))
                    ->boolean()
                    ->trueIcon('heroicon-o-bolt')
                    ->falseIcon('heroicon-o-bolt-slash')
                    ->trueColor('success')
                    ->falseColor('gray'),
                TextColumn::make('redirects_count')
                    ->label(__('Redirects'))
                    ->counts('redirects')
                    ->badge()
                    ->color('info'),
                TextColumn::make('created_at')
                    ->label(__('Created'))
                    ->date('M d, Y')
                    ->sortable()
                    ->toggleable(isToggledHiddenByDefault: true),
            ])
            ->recordActions([
                ActionGroup::make([
                    Action::make('files')
                        ->label(__('Files'))
                        ->icon('heroicon-o-folder')
                        ->color('info')
                        ->action(fn (Domain $record) => $this->openFileManager($record)),
                    Action::make('redirects')
                        ->label(__('Redirects'))
                        ->icon('heroicon-o-arrow-right-circle')
                        ->color('warning')
                        ->modalHeading(fn (Domain $record) => __('Redirects for :domain', ['domain' => $record->domain]))
                        ->modalDescription(__('Redirect this domain to another domain or set up page redirects'))
                        ->modalWidth(Width::FourExtraLarge)
                        ->modalSubmitActionLabel(__('Save Redirects'))
                        ->form(fn (Domain $record) => $this->getRedirectsForm($record))
                        ->fillForm(fn (Domain $record) => $this->getRedirectsFormData($record))
                        ->action(fn (Domain $record, array $data) => $this->saveRedirects($record, $data)),
                    Action::make('aliases')
                        ->label(__('Aliases'))
                        ->icon('heroicon-o-link')
                        ->color('info')
                        ->modalHeading(fn (Domain $record) => __('Aliases for :domain', ['domain' => $record->domain]))
                        ->modalDescription(__('Point additional domains to the same website content.'))
                        ->modalWidth(Width::Large)
                        ->modalSubmitActionLabel(__('Save Aliases'))
                        ->form(fn (Domain $record) => $this->getAliasesForm($record))
                        ->fillForm(fn (Domain $record) => $this->getAliasesFormData($record))
                        ->action(fn (Domain $record, array $data) => $this->saveAliases($record, $data)),
                    Action::make('errorPages')
                        ->label(__('Error Pages'))
                        ->icon('heroicon-o-document-text')
                        ->color('gray')
                        ->modalHeading(fn (Domain $record) => __('Custom Error Pages for :domain', ['domain' => $record->domain]))
                        ->modalDescription(__('Customize the 404, 500 and 503 pages shown to visitors.'))
                        ->modalWidth(Width::TwoExtraLarge)
                        ->modalSubmitActionLabel(__('Save Error Pages'))
                        ->form(fn (Domain $record) => $this->getErrorPagesForm())
                        ->fillForm(fn (Domain $record) => $this->getErrorPagesFormData($record))
                        ->action(fn (Domain $record, array $data) => $this->saveErrorPages($record, $data)),
                    Action::make('hotlink')
                        ->label(__('Hotlink Protection'))
                        ->icon('heroicon-o-shield-check')
                        ->color('success')
                        ->modalHeading(fn (Domain $record) => __('Hotlink Protection for :domain', ['domain' => $record->domain]))
                        ->modalDescription(__('Prevent other websites from directly linking to your files'))
                        ->modalWidth(Width::TwoExtraLarge)
                        ->form($this->getHotlinkForm())
                        ->fillForm(fn (Domain $record) => $this->getHotlinkFormData($record))
                        ->action(fn (Domain $record, array $data) => $this->saveHotlinkSettings($record, $data)),
                    Action::make('index')
                        ->label(__('Index Manager'))
                        ->icon('heroicon-o-document-text')
                        ->color('gray')
                        ->modalHeading(fn (Domain $record) => __('Index Manager for :domain', ['domain' => $record->domain]))
                        ->modalDescription(__('Set the default directory index files'))
                        ->modalWidth(Width::Medium)
                        ->form($this->getIndexForm())
                        ->fillForm(fn (Domain $record) => ['directory_index' => $record->directory_index])
                        ->action(fn (Domain $record, array $data) => $this->saveIndexSettings($record, $data)),
                    Action::make('togglePageCache')
                        ->label(fn (Domain $record) => $record->page_cache_enabled ? __('Disable Page Cache') : __('Enable Page Cache'))
                        ->icon(fn (Domain $record) => $record->page_cache_enabled ? 'heroicon-o-bolt-slash' : 'heroicon-o-bolt')
                        ->color(fn (Domain $record) => $record->page_cache_enabled ? 'warning' : 'success')
                        ->requiresConfirmation()
                        ->modalHeading(fn (Domain $record) => $record->page_cache_enabled ? __('Disable Page Cache') : __('Enable Page Cache'))
                        ->modalDescription(fn (Domain $record) => $record->page_cache_enabled
                            ? __('This will disable nginx page caching for :domain. Pages will no longer be served from cache.', ['domain' => $record->domain])
                            : __('This will enable nginx page caching for :domain. Cached pages will load faster for visitors.', ['domain' => $record->domain]))
                        ->action(fn (Domain $record) => $this->togglePageCache($record)),
                    $this->nginxDirectivesAction()
                        ->modalHeading(fn (Domain $record) => __('Nginx Directives for :domain', ['domain' => $record->domain]))
                        ->modalWidth(Width::FourExtraLarge),
                ])
                    ->label(__('Settings'))
                    ->icon('heroicon-o-cog-6-tooth')
                    ->color('gray')
                    ->button(),
                Action::make('toggle')
                    ->label(fn (Domain $record) => $record->is_active ? __('Disable') : __('Enable'))
                    ->icon(fn (Domain $record) => $record->is_active ? 'heroicon-o-no-symbol' : 'heroicon-o-check')
                    ->color(fn (Domain $record) => $record->is_active ? 'warning' : 'success')
                    ->action(fn (Domain $record) => $this->toggleDomain($record)),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Delete Domain'))
                    ->modalDescription(fn (Domain $record) => __('Are you sure you want to delete')." '{$record->domain}'? ".__('This will also delete the following associated data:'))
                    ->modalIcon('heroicon-o-trash')
                    ->modalIconColor('danger')
                    ->modalSubmitActionLabel(__('Delete Domain'))
                    ->modalWidth(Width::Large)
                    ->form(fn (Domain $record): array => [
                        Toggle::make('delete_files')
                            ->label(__('Delete all domain files'))
                            ->helperText(__('Permanently delete all files in the domain folder'))
                            ->default(true),
                        Toggle::make('delete_dns')
                            ->label(__('Delete DNS records').' ('.$record->dnsRecords()->count().')')
                            ->helperText(__('Remove all DNS records for this domain'))
                            ->default(true)
                            ->visible(fn () => $record->dnsRecords()->exists()),
                        Toggle::make('delete_email')
                            ->label(__('Delete email accounts').' ('.($record->emailDomain?->mailboxes()->count() ?? 0).')')
                            ->helperText(__('Remove all mailboxes and email configuration'))
                            ->default(true)
                            ->visible(fn () => $record->emailDomain()->exists()),
                        Toggle::make('delete_ssl')
                            ->label(__('Delete SSL certificate'))
                            ->helperText(__('Remove SSL certificate for this domain'))
                            ->default(true)
                            ->visible(fn () => $record->sslCertificate()->exists()),
                        Toggle::make('delete_wordpress')
                            ->label(__('Delete WordPress sites'))
                            ->helperText(__('Remove all WordPress installations on this domain'))
                            ->default(true),
                    ])
                    ->action(fn (Domain $record, array $data) => $this->deleteDomain($record, $data)),
            ])
            ->emptyStateHeading(__('No domains yet'))
            ->emptyStateDescription(__('Click "Add Domain" to add your first domain'))
            ->emptyStateIcon('heroicon-o-globe-alt')
            ->striped()
            ->defaultSort('created_at', 'desc');
    }

    protected function getRedirectsForm(Domain $record): array
    {
        return [
            // Domain-wide redirect
            Toggle::make('domain_redirect_enabled')
                ->label(__('Redirect Entire Domain'))
                ->helperText(__('Redirect all traffic from this domain to another domain'))
                ->live()
                ->columnSpanFull(),

            Grid::make()
                ->schema([
                    TextInput::make('domain_redirect_url')
                        ->label(__('Redirect To'))
                        ->placeholder(__('https://newdomain.com'))
                        ->helperText(__('All requests to this domain will be redirected to this URL'))
                        ->url()
                        ->required(fn ($get) => $get('domain_redirect_enabled'))
                        ->columnSpan(['default' => 2, 'md' => 1]),
                    Select::make('domain_redirect_type')
                        ->label(__('Redirect Type'))
                        ->options([
                            '301' => __('Permanent (301) - SEO friendly'),
                            '302' => __('Temporary (302)'),
                        ])
                        ->default('301')
                        ->required(fn ($get) => $get('domain_redirect_enabled'))
                        ->columnSpan(['default' => 2, 'md' => 1]),
                ])
                ->columns(['default' => 2, 'md' => 2])
                ->visible(fn ($get) => $get('domain_redirect_enabled')),

            // Page redirects
            Repeater::make('redirects')
                ->label(__('Page Redirects'))
                ->helperText(__('Redirect specific paths to other URLs'))
                ->schema([
                    Grid::make()
                        ->schema([
                            TextInput::make('source_path')
                                ->label(__('Source Path'))
                                ->placeholder(__('/old-page'))
                                ->helperText(__('Path to redirect from (e.g., /old-page)'))
                                ->required()
                                ->columnSpan(['default' => 2, 'md' => 1]),
                            TextInput::make('destination_url')
                                ->label(__('Destination URL'))
                                ->placeholder(__('https://example.com/new-page'))
                                ->helperText(__('Full URL to redirect to'))
                                ->required()
                                ->url()
                                ->columnSpan(['default' => 2, 'md' => 1]),
                        ])
                        ->columns(['default' => 2, 'md' => 2]),
                    Grid::make()
                        ->schema([
                            Select::make('redirect_type')
                                ->label(__('Type'))
                                ->options([
                                    '301' => __('Permanent (301)'),
                                    '302' => __('Temporary (302)'),
                                ])
                                ->default('301')
                                ->required()
                                ->columnSpan(['default' => 2, 'sm' => 1]),
                            Toggle::make('is_wildcard')
                                ->label(__('Wildcard'))
                                ->helperText(__('Match all paths starting with source'))
                                ->columnSpan(['default' => 2, 'sm' => 1]),
                            Toggle::make('is_active')
                                ->label(__('Active'))
                                ->default(true)
                                ->columnSpan(['default' => 2, 'sm' => 1]),
                        ])
                        ->columns(['default' => 2, 'sm' => 3]),
                ])
                ->itemLabel(fn (array $state): ?string => ($state['source_path'] ?? '').' → '.($state['redirect_type'] ?? '301'))
                ->collapsible()
                ->collapsed(fn () => $record->redirects()->count() > 3)
                ->addActionLabel(__('Add Page Redirect'))
                ->reorderable()
                ->defaultItems(0)
                ->visible(fn ($get) => ! $get('domain_redirect_enabled')),
        ];
    }

    protected function getRedirectsFormData(Domain $record): array
    {
        // Check if there's a domain-wide redirect (source_path = '/*' or '*')
        $domainRedirect = $record->redirects()
            ->whereIn('source_path', ['/*', '*', '/'])
            ->where('is_wildcard', true)
            ->first();

        return [
            'domain_redirect_enabled' => $domainRedirect !== null,
            'domain_redirect_url' => $domainRedirect?->destination_url ?? '',
            'domain_redirect_type' => $domainRedirect?->redirect_type ?? '301',
            'redirects' => $record->redirects()
                ->whereNotIn('source_path', ['/*', '*', '/'])
                ->orWhere('is_wildcard', false)
                ->get()
                ->map(fn ($r) => [
                    'id' => $r->id,
                    'source_path' => $r->source_path,
                    'destination_url' => $r->destination_url,
                    'redirect_type' => $r->redirect_type,
                    'is_wildcard' => $r->is_wildcard,
                    'is_active' => $r->is_active,
                ])->toArray(),
        ];
    }

    protected function getHotlinkForm(): array
    {
        return [
            Toggle::make('is_enabled')
                ->label(__('Enable Hotlink Protection'))
                ->helperText(__('Block other websites from directly linking to your images and files'))
                ->live(),
            Grid::make()
                ->schema([
                    Textarea::make('allowed_domains')
                        ->label(__('Allowed Domains'))
                        ->helperText(__('One domain per line that can link to your files (your own domain is always allowed)'))
                        ->placeholder(__("example.com\ntrusted-site.com"))
                        ->rows(4)
                        ->columnSpan(['default' => 2, 'md' => 1]),
                    TextInput::make('protected_extensions')
                        ->label(__('Protected File Extensions'))
                        ->helperText(__('Comma-separated list of file extensions to protect'))
                        ->placeholder(__('jpg,jpeg,png,gif,webp,svg,mp4,mp3,pdf'))
                        ->default(DomainHotlinkSetting::getDefaultExtensions())
                        ->columnSpan(['default' => 2, 'md' => 1]),
                ])
                ->columns(['default' => 2, 'md' => 2])
                ->visible(fn ($get) => $get('is_enabled')),
            Grid::make()
                ->schema([
                    Toggle::make('block_blank_referrer')
                        ->label(__('Block Blank Referrer'))
                        ->helperText(__('Block requests with no referrer header'))
                        ->default(true)
                        ->columnSpan(['default' => 2, 'md' => 1]),
                    TextInput::make('redirect_url')
                        ->label(__('Redirect URL (Optional)'))
                        ->helperText(__('Redirect blocked requests to this URL instead of showing an error'))
                        ->placeholder(__('https://example.com/hotlink-blocked.png'))
                        ->url()
                        ->columnSpan(['default' => 2, 'md' => 1]),
                ])
                ->columns(['default' => 2, 'md' => 2])
                ->visible(fn ($get) => $get('is_enabled')),
        ];
    }

    protected function getHotlinkFormData(Domain $record): array
    {
        $setting = $record->hotlinkSetting;
        if (! $setting) {
            return [
                'is_enabled' => false,
                'allowed_domains' => '',
                'block_blank_referrer' => true,
                'protected_extensions' => DomainHotlinkSetting::getDefaultExtensions(),
                'redirect_url' => '',
            ];
        }

        return [
            'is_enabled' => $setting->is_enabled,
            'allowed_domains' => $setting->allowed_domains,
            'block_blank_referrer' => $setting->block_blank_referrer,
            'protected_extensions' => $setting->protected_extensions,
            'redirect_url' => $setting->redirect_url ?? '',
        ];
    }

    protected function getIndexForm(): array
    {
        return [
            Radio::make('directory_index')
                ->label(__('Directory Index Priority'))
                ->helperText(__('Choose which file should be served as the default index'))
                ->options([
                    'index.php index.html' => __('PHP first (index.php, then index.html)'),
                    'index.html index.php' => __('HTML first (index.html, then index.php)'),
                    'index.php' => __('PHP only (index.php)'),
                    'index.html' => __('HTML only (index.html)'),
                    'index.php index.html index.htm' => __('PHP, HTML, HTM (full support)'),
                ])
                ->default('index.php index.html')
                ->required(),
        ];
    }

    protected function getHeaderActions(): array
    {
        return [
            $this->createDomainAction(),
        ];
    }

    protected function createDomainAction(): Action
    {
        return Action::make('createDomain')
            ->label(__('Add Domain'))
            ->icon('heroicon-o-plus-circle')
            ->color('primary')
            ->modalHeading(__('Add Domain'))
            ->modalDescription(__('Add a new domain to your hosting account'))
            ->modalIcon('heroicon-o-globe-alt')
            ->modalIconColor('primary')
            ->modalSubmitActionLabel(__('Add Domain'))
            ->form([
                TextInput::make('domain')
                    ->label(__('Domain Name'))
                    ->placeholder(__('example.com'))
                    ->required()
                    ->regex('/^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*\.[a-zA-Z]{2,}$/')
                    ->helperText(__('Enter the domain name without http:// or www')),
            ])
            ->action(function (array $data): void {
                try {
                    $user = Auth::user();
                    $limit = $user?->hostingPackage?->domains_limit;
                    if ($limit && Domain::where('user_id', $user->id)->count() >= $limit) {
                        Notification::make()
                            ->title(__('Domain limit reached'))
                            ->body(__('Your hosting package allows up to :limit domains.', ['limit' => $limit]))
                            ->warning()
                            ->send();

                        return;
                    }

                    $result = $this->agent()->domainCreate($this->getUsername(), $data['domain']);

                    if ($result['success'] ?? false) {
                        $created = Domain::create([
                            'user_id' => Auth::id(),
                            'domain' => $data['domain'],
                            'document_root' => '/home/'.$this->getUsername().'/domains/'.$data['domain'].'/public_html',
                            'is_active' => true,
                            'ssl_enabled' => false,
                            'directory_index' => 'index.php index.html',
                            'page_cache_enabled' => false,
                        ]);

                        AuditLog::logDomainAction('created', $created->domain, $created->id);

                        Notification::make()
                            ->title(__('Domain created!'))
                            ->body(__('Your domain is now active.'))
                            ->success()
                            ->send();
                    } else {
                        throw new \RuntimeException($result['error'] ?? 'Unknown error');
                    }
                } catch (\Throwable $e) {
                    \Illuminate\Support\Facades\Log::error('[domain-create] '.$e->getMessage(), [
                        'exception' => get_class($e),
                        'file' => $e->getFile(),
                        'line' => $e->getLine(),
                    ]);

                    Notification::make()
                        ->title(__('Error creating domain'))
                        ->body(SafeError::message($e))
                        ->danger()
                        ->send();
                }
            });
    }

    public function saveRedirects(Domain $domain, array $data): void
    {
        try {
            $existingIds = [];

            // Handle domain-wide redirect
            if ($data['domain_redirect_enabled'] ?? false) {
                // Delete all existing redirects and create a single domain-wide redirect
                $domain->redirects()->delete();

                $redirect = $domain->redirects()->create([
                    'source_path' => '/*',
                    'destination_url' => $data['domain_redirect_url'],
                    'redirect_type' => $data['domain_redirect_type'] ?? '301',
                    'is_wildcard' => true,
                    'is_active' => true,
                ]);
                $existingIds[] = $redirect->id;
            } else {
                // Delete any domain-wide redirects
                $domain->redirects()
                    ->whereIn('source_path', ['/*', '*', '/'])
                    ->where('is_wildcard', true)
                    ->delete();

                // Handle page redirects
                $redirectsData = $data['redirects'] ?? [];

                foreach ($redirectsData as $redirectData) {
                    if (! empty($redirectData['id'])) {
                        $redirect = DomainRedirect::find($redirectData['id']);
                        if ($redirect && $redirect->domain_id === $domain->id) {
                            $redirect->update([
                                'source_path' => $redirectData['source_path'],
                                'destination_url' => $redirectData['destination_url'],
                                'redirect_type' => $redirectData['redirect_type'],
                                'is_wildcard' => $redirectData['is_wildcard'] ?? false,
                                'is_active' => $redirectData['is_active'] ?? true,
                            ]);
                            $existingIds[] = $redirect->id;
                        }
                    } else {
                        $redirect = $domain->redirects()->create([
                            'source_path' => $redirectData['source_path'],
                            'destination_url' => $redirectData['destination_url'],
                            'redirect_type' => $redirectData['redirect_type'],
                            'is_wildcard' => $redirectData['is_wildcard'] ?? false,
                            'is_active' => $redirectData['is_active'] ?? true,
                        ]);
                        $existingIds[] = $redirect->id;
                    }
                }

                // Delete removed page redirects (but not domain-wide ones which we already handled)
                if (! empty($existingIds)) {
                    $domain->redirects()
                        ->whereNotIn('id', $existingIds)
                        ->whereNotIn('source_path', ['/*', '*', '/'])
                        ->delete();
                }
            }

            // Apply redirects via agent
            $this->applyRedirects($domain);

            Notification::make()
                ->title(__('Redirects saved!'))
                ->body(__('Your redirect rules have been updated.'))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error saving redirects'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    protected function applyRedirects(Domain $domain): void
    {
        $redirects = $domain->redirects()->where('is_active', true)->get()->map(fn ($r) => [
            'source' => $r->source_path,
            'destination' => $r->destination_url,
            'type' => $r->redirect_type,
            'wildcard' => $r->is_wildcard,
        ])->toArray();

        $this->agent()->send('domain.set_redirects', [
            'username' => $this->getUsername(),
            'domain' => $domain->domain,
            'redirects' => $redirects,
        ]);
    }

    public function saveHotlinkSettings(Domain $domain, array $data): void
    {
        try {
            $setting = $domain->hotlinkSetting;
            if (! $setting) {
                $setting = new DomainHotlinkSetting(['domain_id' => $domain->id]);
            }

            $setting->fill([
                'is_enabled' => $data['is_enabled'] ?? false,
                'allowed_domains' => $data['allowed_domains'] ?? '',
                'block_blank_referrer' => $data['block_blank_referrer'] ?? true,
                'protected_extensions' => $data['protected_extensions'] ?? DomainHotlinkSetting::getDefaultExtensions(),
                'redirect_url' => $data['redirect_url'] ?? null,
            ]);
            $setting->save();

            // Apply hotlink protection via agent
            $this->agent()->send('domain.set_hotlink_protection', [
                'username' => $this->getUsername(),
                'domain' => $domain->domain,
                'enabled' => $setting->is_enabled,
                'allowed_domains' => $setting->getAllowedDomainsArray(),
                'block_blank_referrer' => $setting->block_blank_referrer,
                'protected_extensions' => $setting->getProtectedExtensionsArray(),
                'redirect_url' => $setting->redirect_url,
            ]);

            Notification::make()
                ->title(__('Hotlink protection updated!'))
                ->body($setting->is_enabled ? __('Protection is now active.') : __('Protection has been disabled.'))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error saving hotlink settings'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    public function saveIndexSettings(Domain $domain, array $data): void
    {
        try {
            $domain->update(['directory_index' => $data['directory_index']]);

            // Apply index settings via agent
            $this->agent()->send('domain.set_directory_index', [
                'username' => $this->getUsername(),
                'domain' => $domain->domain,
                'directory_index' => $data['directory_index'],
            ]);

            Notification::make()
                ->title(__('Index settings updated!'))
                ->body(__('Directory index priority has been changed.'))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error saving index settings'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    public function toggleDomain(Domain $domain): void
    {
        $newStatus = ! $domain->is_active;
        $label = $newStatus ? 'Domain Enabled' : 'Domain Disabled';

        $this->agentCall(
            action: 'domain.toggle',
            params: [
                'username' => $this->getUsername(),
                'domain' => $domain->domain,
                'enable' => $newStatus,
            ],
            successTitle: $label,
            errorTitle: 'Error toggling domain',
            onSuccess: function () use ($domain, $newStatus): void {
                $domain->update(['is_active' => $newStatus]);
            },
        );
    }

    public function togglePageCache(Domain $domain): void
    {
        $newStatus = ! $domain->page_cache_enabled;
        $action = $newStatus ? 'wp.page_cache_enable' : 'wp.page_cache_disable';
        $label = $newStatus ? __('Page Cache Enabled') : __('Page Cache Disabled');

        $this->agentCall(
            action: $action,
            params: [
                'username' => $this->getUsername(),
                'domain' => $domain->domain,
            ],
            successTitle: $label,
            errorTitle: __('Error toggling page cache'),
            onSuccess: function () use ($domain, $newStatus): void {
                $domain->update(['page_cache_enabled' => $newStatus]);
            },
        );
    }

    public function deleteDomain(Domain $domain, array $options): void
    {
        $deletedItems = [];
        $errors = [];

        try {
            // Delete WordPress sites first (via Agent)
            if ($options['delete_wordpress'] ?? false) {
                try {
                    $wpResult = $this->agent()->send('wp.list', [
                        'username' => $this->getUsername(),
                    ]);

                    foreach ($wpResult['sites'] ?? [] as $site) {
                        if (($site['domain'] ?? '') === $domain->domain) {
                            $this->agent()->send('wp.delete', [
                                'username' => $this->getUsername(),
                                'site_id' => $site['id'],
                                'delete_files' => true,
                                'delete_database' => true,
                            ]);
                            $deletedItems[] = __('WordPress site');
                        }
                    }
                } catch (Exception $e) {
                    $errors[] = __('WordPress: ').$e->getMessage();
                }
            }

            // Delete SSL certificate
            if ($options['delete_ssl'] ?? false) {
                if ($domain->sslCertificate) {
                    try {
                        $this->agent()->send('ssl.delete', [
                            'username' => $this->getUsername(),
                            'domain' => $domain->domain,
                        ]);
                        $domain->sslCertificate->delete();
                        $deletedItems[] = __('SSL certificate');
                    } catch (Exception $e) {
                        $errors[] = __('SSL: ').$e->getMessage();
                    }
                }
            }

            // Delete email accounts
            if ($options['delete_email'] ?? false) {
                if ($domain->emailDomain) {
                    try {
                        foreach ($domain->emailDomain->mailboxes as $mailbox) {
                            $this->agent()->send('email.mailbox_delete', [
                                'username' => $this->getUsername(),
                                'email' => $mailbox->email,
                                'delete_files' => true,
                                'maildir_path' => $mailbox->maildir_path,
                            ]);
                        }
                        $this->agent()->send('email.disable_domain', [
                            'username' => $this->getUsername(),
                            'domain' => $domain->domain,
                        ]);

                        $mailboxCount = $domain->emailDomain->mailboxes()->count();
                        $domain->emailDomain->mailboxes()->delete();
                        $domain->emailDomain->delete();
                        $deletedItems[] = __(':count email account(s)', ['count' => $mailboxCount]);
                    } catch (Exception $e) {
                        $errors[] = __('Email: ').$e->getMessage();
                    }
                }
            }

            // Delete DNS records
            if ($options['delete_dns'] ?? false) {
                try {
                    $dnsCount = $domain->dnsRecords()->count();
                    if ($dnsCount > 0) {
                        $this->agent()->send('dns.delete_zone', [
                            'domain' => $domain->domain,
                        ]);
                        $domain->dnsRecords()->delete();
                        $deletedItems[] = __(':count DNS record(s)', ['count' => $dnsCount]);
                    }
                } catch (Exception $e) {
                    $errors[] = __('DNS: ').$e->getMessage();
                }
            }

            // Delete redirects and hotlink settings (cascade should handle this, but be explicit)
            $domain->redirects()->delete();
            $domain->hotlinkSetting?->delete();

            // Delete domain files and configuration via Agent
            $result = $this->agent()->domainDelete(
                $this->getUsername(),
                $domain->domain,
                $options['delete_files'] ?? false
            );

            if ($result['success'] ?? false) {
                $domain->delete();

                AuditLog::logDomainAction('deleted', $domain->domain, $domain->id, [
                    'deleted_items' => $deletedItems,
                ]);

                $message = __('Domain deleted successfully.');
                if (! empty($deletedItems)) {
                    $message .= ' '.__('Also deleted: ').implode(', ', $deletedItems);
                }

                Notification::make()
                    ->title(__('Domain deleted'))
                    ->body($message)
                    ->success()
                    ->send();

                if (! empty($errors)) {
                    Notification::make()
                        ->title(__('Some items had warnings'))
                        ->body(implode("\n", $errors))
                        ->warning()
                        ->send();
                }
            } else {
                throw new Exception($result['error'] ?? 'Unknown error');
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error deleting domain'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    public function openFileManager(Domain $domain): void
    {
        $path = str_replace('/home/'.$this->getUsername().'/', '', $domain->document_root);
        $this->redirect(route('filament.jabali.pages.files', ['path' => $path]));
    }

    protected function getAliasesForm(Domain $record): array
    {
        return [
            Repeater::make('aliases')
                ->label(__('Domain Aliases'))
                ->schema([
                    TextInput::make('alias')
                        ->label(__('Alias Domain'))
                        ->placeholder(__('alias-example.com'))
                        ->required()
                        ->rule('regex:/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*\\.[a-z]{2,}$/')
                        ->helperText(__('Enter a full domain name.')),
                ])
                ->addActionLabel(__('Add Alias'))
                ->columns(1)
                ->defaultItems(0),
        ];
    }

    protected function getAliasesFormData(Domain $record): array
    {
        return [
            'aliases' => $record->aliases()
                ->orderBy('alias')
                ->get()
                ->map(fn (DomainAlias $alias) => ['alias' => $alias->alias])
                ->toArray(),
        ];
    }

    protected function saveAliases(Domain $record, array $data): void
    {
        $aliases = collect($data['aliases'] ?? [])
            ->pluck('alias')
            ->map(fn ($alias) => strtolower(trim((string) $alias)))
            ->filter()
            ->unique()
            ->values();

        $existing = $record->aliases()->pluck('alias')->map(fn ($alias) => strtolower($alias));

        $toAdd = $aliases->diff($existing);
        $toRemove = $existing->diff($aliases);

        $errors = [];

        foreach ($toAdd as $alias) {
            try {
                $result = $this->agent()->domainAliasAdd($this->getUsername(), $record->domain, $alias);
                if (! ($result['success'] ?? false)) {
                    throw new Exception($result['error'] ?? 'Failed to add alias');
                }
                DomainAlias::firstOrCreate([
                    'domain_id' => $record->id,
                    'alias' => $alias,
                ]);
            } catch (Exception $e) {
                $errors[] = $alias.': '.$e->getMessage();
            }
        }

        foreach ($toRemove as $alias) {
            try {
                $result = $this->agent()->domainAliasRemove($this->getUsername(), $record->domain, $alias);
                if (! ($result['success'] ?? false)) {
                    throw new Exception($result['error'] ?? 'Failed to remove alias');
                }
                $record->aliases()->where('alias', $alias)->delete();
            } catch (Exception $e) {
                $errors[] = $alias.': '.$e->getMessage();
            }
        }

        if (! empty($errors)) {
            Notification::make()
                ->title(__('Some aliases failed'))
                ->body(implode("\n", $errors))
                ->warning()
                ->send();

            return;
        }

        Notification::make()
            ->title(__('Aliases updated'))
            ->success()
            ->send();
    }

    protected function getErrorPagesForm(): array
    {
        return [
            Textarea::make('page_404')
                ->label(__('404 Page (Not Found)'))
                ->rows(6)
                ->placeholder(__('HTML content for your 404 page')),
            Textarea::make('page_500')
                ->label(__('500 Page (Server Error)'))
                ->rows(6)
                ->placeholder(__('HTML content for your 500 page')),
            Textarea::make('page_503')
                ->label(__('503 Page (Maintenance)'))
                ->rows(6)
                ->placeholder(__('HTML content for your 503 page')),
        ];
    }

    protected function getErrorPagesFormData(Domain $record): array
    {
        return [
            'page_404' => $this->readErrorPageContent($record, '404.html'),
            'page_500' => $this->readErrorPageContent($record, '500.html'),
            'page_503' => $this->readErrorPageContent($record, '503.html'),
        ];
    }

    protected function saveErrorPages(Domain $record, array $data): void
    {
        try {
            $result = $this->agent()->domainEnsureErrorPages($this->getUsername(), $record->domain);
            if (! ($result['success'] ?? false)) {
                throw new Exception($result['error'] ?? 'Failed to enable error pages');
            }

            foreach (['404' => 'page_404', '500' => 'page_500', '503' => 'page_503'] as $code => $key) {
                $content = trim((string) ($data[$key] ?? ''));
                $path = $this->getErrorPagePath($record, "{$code}.html");

                if ($content === '') {
                    try {
                        $this->agent()->fileDelete($this->getUsername(), $path);
                    } catch (Exception) {
                        // Ignore missing file
                    }

                    continue;
                }

                $this->agent()->fileWrite($this->getUsername(), $path, $content);
            }

            Notification::make()
                ->title(__('Error pages updated'))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Failed to update error pages'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    protected function readErrorPageContent(Domain $record, string $filename): string
    {
        $path = $this->getErrorPagePath($record, $filename);
        try {
            $result = $this->agent()->fileRead($this->getUsername(), $path);
            if ($result['success'] ?? false) {
                return (string) base64_decode($result['content'] ?? '');
            }
        } catch (Exception) {
            // ignore
        }

        return '';
    }

    protected function getErrorPagePath(Domain $record, string $filename): string
    {
        return "domains/{$record->domain}/public_html/{$filename}";
    }
}
