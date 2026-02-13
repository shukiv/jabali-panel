<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Jobs\RunGitDeployment;
use App\Models\Domain;
use App\Models\GitDeployment as GitDeploymentModel;
use App\Services\Agent\AgentClient;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Str;

class GitDeployment extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-code-bracket-square';

    protected static ?int $navigationSort = 16;

    protected static ?string $slug = 'git-deployment';

    protected string $view = 'filament.jabali.pages.git-deployment';

    protected ?AgentClient $agent = null;

    public ?string $deployKey = null;

    public function getTitle(): string|Htmlable
    {
        return __('Git Deployment');
    }

    public static function getNavigationLabel(): string
    {
        return __('Git Deployment');
    }

    protected function getAgent(): AgentClient
    {
        if ($this->agent === null) {
            $this->agent = new AgentClient;
        }

        return $this->agent;
    }

    protected function getUsername(): string
    {
        return Auth::user()->username;
    }

    protected function getDomainOptions(): array
    {
        return Domain::query()
            ->where('user_id', Auth::id())
            ->orderBy('domain')
            ->pluck('domain', 'id')
            ->toArray();
    }

    protected function getWebhookUrl(GitDeploymentModel $deployment): string
    {
        return url("/api/webhooks/git/{$deployment->id}");
    }

    protected function getDeployKey(): string
    {
        if ($this->deployKey) {
            return $this->deployKey;
        }

        try {
            $result = $this->getAgent()->gitGenerateKey($this->getUsername());
            $this->deployKey = $result['public_key'] ?? '';
        } catch (Exception) {
            $this->deployKey = '';
        }

        return $this->deployKey ?? '';
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(GitDeploymentModel::query()->where('user_id', Auth::id())->with('domain'))
            ->columns([
                TextColumn::make('domain.domain')
                    ->label(__('Domain'))
                    ->searchable()
                    ->sortable(),
                TextColumn::make('repo_url')
                    ->label(__('Repository'))
                    ->limit(40)
                    ->tooltip(fn (GitDeploymentModel $record) => $record->repo_url)
                    ->sortable(),
                TextColumn::make('branch')
                    ->label(__('Branch'))
                    ->badge()
                    ->color('gray'),
                IconColumn::make('auto_deploy')
                    ->label(__('Auto Deploy'))
                    ->boolean(),
                TextColumn::make('last_status')
                    ->label(__('Status'))
                    ->badge()
                    ->color(fn (?string $state): string => match ($state) {
                        'success' => 'success',
                        'failed' => 'danger',
                        'running' => 'warning',
                        'queued' => 'info',
                        default => 'gray',
                    })
                    ->default('never'),
                TextColumn::make('last_deployed_at')
                    ->label(__('Last Deployed'))
                    ->since()
                    ->sortable(),
            ])
            ->recordActions([
                Action::make('deploy')
                    ->label(__('Deploy'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('primary')
                    ->action(function (GitDeploymentModel $record): void {
                        $record->update(['last_status' => 'queued']);
                        RunGitDeployment::dispatch($record->id);
                        Notification::make()->title(__('Deployment queued'))->success()->send();
                    }),
                Action::make('webhook')
                    ->label(__('Webhook'))
                    ->icon('heroicon-o-link')
                    ->color('gray')
                    ->modalHeading(__('Webhook URL'))
                    ->modalSubmitAction(false)
                    ->modalCancelActionLabel(__('Close'))
                    ->form([
                        Textarea::make('webhook_url')
                            ->label(__('Webhook URL'))
                            ->rows(2)
                            ->disabled()
                            ->dehydrated(false),
                        TextInput::make('webhook_secret')
                            ->label(__('Webhook Secret'))
                            ->helperText(__('Set this as your provider webhook secret. Jabali validates HMAC-SHA256 signatures.'))
                            ->disabled()
                            ->dehydrated(false),
                        Textarea::make('deploy_key')
                            ->label(__('Deploy Key'))
                            ->rows(3)
                            ->disabled()
                            ->dehydrated(false),
                    ])
                    ->fillForm(fn (GitDeploymentModel $record): array => [
                        'webhook_url' => $this->getWebhookUrl($record),
                        'webhook_secret' => $record->secret_token,
                        'deploy_key' => $this->getDeployKey(),
                    ]),
                Action::make('edit')
                    ->label(__('Edit'))
                    ->icon('heroicon-o-pencil-square')
                    ->color('gray')
                    ->modalHeading(__('Edit Deployment'))
                    ->form($this->getDeploymentForm())
                    ->fillForm(fn (GitDeploymentModel $record): array => [
                        'domain_id' => $record->domain_id,
                        'repo_url' => $record->repo_url,
                        'branch' => $record->branch,
                        'deploy_path' => $record->deploy_path,
                        'auto_deploy' => $record->auto_deploy,
                        'deploy_script' => $record->deploy_script,
                        'framework_preset' => 'custom',
                    ])
                    ->action(function (GitDeploymentModel $record, array $data): void {
                        $record->update([
                            'domain_id' => $data['domain_id'],
                            'repo_url' => $data['repo_url'],
                            'branch' => $data['branch'],
                            'deploy_path' => $data['deploy_path'],
                            'auto_deploy' => $data['auto_deploy'] ?? false,
                            'deploy_script' => $data['deploy_script'] ?? null,
                        ]);

                        Notification::make()->title(__('Deployment updated'))->success()->send();
                    }),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->action(function (GitDeploymentModel $record): void {
                        $record->delete();
                        Notification::make()->title(__('Deployment deleted'))->success()->send();
                    }),
            ])
            ->emptyStateHeading(__('No deployments yet'))
            ->emptyStateDescription(__('Add a repository to deploy to your domain'))
            ->emptyStateIcon('heroicon-o-code-bracket-square');
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('addDeployment')
                ->label(__('Add Deployment'))
                ->icon('heroicon-o-plus')
                ->color('primary')
                ->form($this->getDeploymentForm())
                ->action(function (array $data): void {
                    GitDeploymentModel::create([
                        'user_id' => Auth::id(),
                        'domain_id' => $data['domain_id'],
                        'repo_url' => $data['repo_url'],
                        'branch' => $data['branch'],
                        'deploy_path' => $data['deploy_path'],
                        'auto_deploy' => $data['auto_deploy'] ?? false,
                        'deploy_script' => $data['deploy_script'] ?? null,
                        'secret_token' => Str::random(40),
                        'last_status' => 'never',
                    ]);

                    Notification::make()->title(__('Deployment created'))->success()->send();
                    $this->resetTable();
                }),
            Action::make('deployKey')
                ->label(__('Deploy Key'))
                ->icon('heroicon-o-key')
                ->color('gray')
                ->modalHeading(__('SSH Deploy Key'))
                ->modalSubmitAction(false)
                ->modalCancelActionLabel(__('Close'))
                ->form([
                    Textarea::make('public_key')
                        ->label(__('Public Key'))
                        ->rows(3)
                        ->disabled()
                        ->dehydrated(false),
                ])
                ->fillForm(fn (): array => ['public_key' => $this->getDeployKey()]),
        ];
    }

    protected function getDeploymentForm(): array
    {
        return [
            Select::make('domain_id')
                ->label(__('Domain'))
                ->options($this->getDomainOptions())
                ->searchable()
                ->required()
                ->live()
                ->afterStateUpdated(function ($state, callable $set): void {
                    if (! $state) {
                        return;
                    }
                    $domain = Domain::where('id', $state)->where('user_id', Auth::id())->first();
                    if ($domain) {
                        $set('deploy_path', $domain->document_root);
                    }
                }),
            TextInput::make('repo_url')
                ->label(__('Repository URL'))
                ->placeholder(__('git@github.com:org/repo.git'))
                ->required(),
            TextInput::make('branch')
                ->label(__('Branch'))
                ->default('main')
                ->required(),
            TextInput::make('deploy_path')
                ->label(__('Deploy Path'))
                ->helperText(__('Must be inside your home directory'))
                ->required(),
            Select::make('framework_preset')
                ->label(__('Framework Preset'))
                ->options([
                    'custom' => __('Custom'),
                    'laravel' => __('Laravel'),
                    'symfony' => __('Symfony'),
                ])
                ->default('custom')
                ->live()
                ->afterStateUpdated(function ($state, callable $set): void {
                    if ($state === 'laravel') {
                        $set('deploy_script', "composer install --no-dev --optimize-autoloader\nphp artisan migrate --force\nphp artisan config:cache\nphp artisan route:cache\nphp artisan view:cache");
                    } elseif ($state === 'symfony') {
                        $set('deploy_script', "composer install --no-dev --optimize-autoloader\nphp bin/console cache:clear --no-warmup\nphp bin/console cache:warmup");
                    }
                }),
            Textarea::make('deploy_script')
                ->label(__('Deploy Script (optional)'))
                ->rows(6)
                ->helperText(__('Run after code is deployed. Leave empty to skip.')),
            Toggle::make('auto_deploy')
                ->label(__('Enable auto-deploy from webhook'))
                ->default(false),
        ];
    }
}
