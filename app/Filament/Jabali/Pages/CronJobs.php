<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Models\CronJob;
use App\Models\Domain;
use App\Services\Agent\AgentClient;
use App\Support\SafeError;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Placeholder;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
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
use Illuminate\Support\HtmlString;

class CronJobs extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-clock';

    protected static ?int $navigationSort = 10;

    public static function getNavigationLabel(): string
    {
        return __('Cron Jobs');
    }

    protected string $view = 'filament.jabali.pages.cron-jobs';

    public array $wordPressDomains = [];

    public function getTitle(): string|Htmlable
    {
        return __('Cron Jobs');
    }

    public function getAgent(): AgentClient
    {
        return app(AgentClient::class);
    }

    public function getUsername(): string
    {
        return Auth::user()->username;
    }

    public function mount(): void
    {
        $this->loadWordPressDomains();
    }

    public function loadWordPressDomains(): void
    {
        try {
            // Get WordPress sites from the agent
            $result = $this->getAgent()->wpList($this->getUsername());
            $sites = $result['sites'] ?? [];

            // Build options array: domain_id => domain name (with path if subdirectory install)
            $this->wordPressDomains = [];
            foreach ($sites as $site) {
                $domain = Domain::where('user_id', Auth::id())
                    ->where('domain', $site['domain'])
                    ->first();

                if ($domain) {
                    $label = $site['domain'];
                    if (! empty($site['path'])) {
                        $label .= '/'.$site['path'];
                    }
                    $this->wordPressDomains[$domain->id] = $label;
                }
            }
        } catch (Exception $e) {
            $this->wordPressDomains = [];
        }
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(CronJob::query()->where('user_id', Auth::id())->orderBy('created_at', 'desc'))
            ->columns([
                TextColumn::make('name')
                    ->label(__('Job Name'))
                    ->icon('heroicon-o-clock')
                    ->iconColor('primary')
                    ->description(fn (CronJob $record) => $record->command)
                    ->searchable()
                    ->sortable(),
                TextColumn::make('schedule')
                    ->label(__('Schedule'))
                    ->fontFamily('mono')
                    ->description(fn (CronJob $record) => $record->schedule_human),
                TextColumn::make('type')
                    ->label(__('Type'))
                    ->badge()
                    ->color(fn (string $state) => $state === 'wordpress' ? 'info' : 'gray')
                    ->formatStateUsing(fn (string $state) => $state === 'wordpress' ? __('WordPress') : __('Custom')),
                IconColumn::make('is_active')
                    ->label(__('Status'))
                    ->boolean()
                    ->trueIcon('heroicon-o-check-circle')
                    ->falseIcon('heroicon-o-pause-circle')
                    ->trueColor('success')
                    ->falseColor('gray'),
                TextColumn::make('last_run_at')
                    ->label(__('Last Run'))
                    ->since()
                    ->description(fn (CronJob $record) => $record->last_run_status ? ($record->last_run_status === 'success' ? __('Success') : __('Failed')) : null)
                    ->placeholder(__('Never'))
                    ->sortable(),
            ])
            ->recordActions([
                Action::make('run')
                    ->label(__('Run Now'))
                    ->icon('heroicon-o-play')
                    ->color('info')
                    ->action(fn (CronJob $record) => $this->runCronJob($record->id)),
                Action::make('viewOutput')
                    ->label(__('View Output'))
                    ->icon('heroicon-o-document-text')
                    ->color('gray')
                    ->visible(fn (CronJob $record) => $record->last_run_at !== null)
                    ->modalHeading(__('Last Run Output'))
                    ->modalDescription(fn (CronJob $record) => __('Last run: :time - Status: :status', [
                        'time' => $record->last_run_at?->diffForHumans(),
                        'status' => $record->last_run_status === 'success' ? __('Success') : __('Failed'),
                    ]))
                    ->modalIcon(fn (CronJob $record) => $record->last_run_status === 'success' ? 'heroicon-o-check-circle' : 'heroicon-o-x-circle')
                    ->modalIconColor(fn (CronJob $record) => $record->last_run_status === 'success' ? 'success' : 'danger')
                    ->modalContent(fn (CronJob $record) => view('filament.jabali.components.cron-output', ['output' => $record->last_run_output]))
                    ->modalSubmitAction(false)
                    ->modalCancelActionLabel(__('Close')),
                Action::make('toggle')
                    ->label(fn (CronJob $record) => $record->is_active ? __('Disable') : __('Enable'))
                    ->icon(fn (CronJob $record) => $record->is_active ? 'heroicon-o-pause' : 'heroicon-o-play-circle')
                    ->color('warning')
                    ->action(fn (CronJob $record) => $this->toggleCronJob($record->id)),
                Action::make('edit')
                    ->label(__('Edit'))
                    ->icon('heroicon-o-pencil')
                    ->color('gray')
                    ->visible(fn (CronJob $record) => $record->type === 'custom')
                    ->modalHeading(__('Edit Cron Job'))
                    ->modalDescription(fn (CronJob $record) => $record->name)
                    ->modalIcon('heroicon-o-pencil')
                    ->modalIconColor('gray')
                    ->modalSubmitActionLabel(__('Save Changes'))
                    ->fillForm(fn (CronJob $record) => [
                        'name' => $record->name,
                        'schedule' => $record->schedule,
                        'command' => $record->command,
                    ])
                    ->form([
                        TextInput::make('name')
                            ->label(__('Job Name'))
                            ->required()
                            ->maxLength(255)
                            ->helperText(__('A friendly name to identify this cron job')),
                        Select::make('schedule')
                            ->label(__('Schedule'))
                            ->options(CronJob::scheduleOptions())
                            ->required()
                            ->searchable()
                            ->helperText(__('How often the command should run')),
                        Textarea::make('command')
                            ->label(__('Command'))
                            ->required()
                            ->rows(3)
                            ->helperText(__('The command will run as your user account')),
                    ])
                    ->action(fn (CronJob $record, array $data) => $this->updateCronJob($record, $data)),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Delete Cron Job'))
                    ->modalDescription(fn (CronJob $record) => __('Are you sure you want to delete')." '{$record->name}'?")
                    ->modalIcon('heroicon-o-trash')
                    ->modalIconColor('danger')
                    ->modalSubmitActionLabel(__('Delete Cron Job'))
                    ->action(fn (CronJob $record) => $this->deleteCronJob($record->id)),
            ])
            ->emptyStateHeading(__('No cron jobs'))
            ->emptyStateDescription(__('Get started by creating a new cron job or setting up WordPress cron.'))
            ->emptyStateIcon('heroicon-o-clock')
            ->striped();
    }

    public function createCronJobAction(): Action
    {
        return Action::make('createCronJob')
            ->label(__('Add Cron Job'))
            ->icon('heroicon-o-plus')
            ->modalHeading(__('Create Cron Job'))
            ->modalDescription(__('Schedule a command to run automatically at specified intervals'))
            ->modalIcon('heroicon-o-clock')
            ->modalIconColor('primary')
            ->modalSubmitActionLabel(__('Create Cron Job'))
            ->modalWidth('lg')
            ->form([
                TextInput::make('name')
                    ->label(__('Job Name'))
                    ->required()
                    ->maxLength(255)
                    ->placeholder(__('My scheduled task'))
                    ->helperText(__('A friendly name to identify this cron job')),
                Select::make('schedule')
                    ->label(__('Schedule'))
                    ->options(CronJob::scheduleOptions())
                    ->required()
                    ->searchable()
                    ->helperText(__('How often the command should run')),
                Textarea::make('command')
                    ->label(__('Command'))
                    ->required()
                    ->rows(3)
                    ->placeholder(__('php /home/user/script.php'))
                    ->helperText(__('The command will run as your user account')),
                Placeholder::make('warning')
                    ->content(new HtmlString('
                        <div class="rounded-lg bg-warning-500/10 p-4 text-sm text-warning-600 dark:text-warning-400">
                            <strong>'.__('Warning:').'</strong> '.__('Cron jobs run automatically at scheduled times. Misconfigured jobs can:').'
                            <ul class="list-disc list-inside mt-2 space-y-1">
                                <li>'.__('Consume excessive server resources').'</li>
                                <li>'.__('Send spam emails if configured incorrectly').'</li>
                                <li>'.__('Cause database locks or corruption').'</li>
                                <li>'.__('Fill up disk space with log files').'</li>
                            </ul>
                            <p class="mt-2">'.__('Only create cron jobs if you understand what the command does.').'</p>
                        </div>
                    ')),
            ])
            ->action(function (array $data): void {
                try {
                    $this->validateCronCommand($data['command']);

                    // Create in database - Laravel scheduler will handle execution
                    CronJob::create([
                        'user_id' => Auth::id(),
                        'name' => $data['name'],
                        'schedule' => $data['schedule'],
                        'command' => $data['command'],
                        'type' => 'custom',
                        'is_active' => true,
                    ]);

                    Notification::make()
                        ->title(__('Cron job created'))
                        ->body(__('The job will run according to its schedule.'))
                        ->success()
                        ->send();
                } catch (Exception $e) {
                    Notification::make()
                        ->title(__('Error creating cron job'))
                        ->body(SafeError::message($e))
                        ->danger()
                        ->send();
                }
            });
    }

    public function setupWordPressCronAction(): Action
    {
        return Action::make('setupWordPressCron')
            ->label(__('Setup WordPress Cron'))
            ->icon('heroicon-o-bolt')
            ->color('info')
            ->modalHeading(__('Setup WordPress Cron'))
            ->modalDescription(__('Replace WordPress\'s built-in cron with a real server cron job for better reliability and performance'))
            ->modalIcon('heroicon-o-bolt')
            ->modalIconColor('info')
            ->modalSubmitActionLabel(__('Setup WordPress Cron'))
            ->modalWidth('lg')
            ->form([
                Select::make('domain_id')
                    ->label(__('WordPress Site'))
                    ->options($this->wordPressDomains)
                    ->required()
                    ->searchable()
                    ->placeholder(__('Select a WordPress site'))
                    ->helperText(__('Select the WordPress site to enable server-side cron for')),
                Select::make('schedule')
                    ->label(__('Run Frequency'))
                    ->options([
                        '*/5 * * * *' => __('Every 5 minutes (Recommended)'),
                        '*/10 * * * *' => __('Every 10 minutes'),
                        '*/15 * * * *' => __('Every 15 minutes'),
                        '*/30 * * * *' => __('Every 30 minutes'),
                        '0 * * * *' => __('Every hour'),
                    ])
                    ->default('*/5 * * * *')
                    ->required()
                    ->helperText(__('How often WordPress scheduled tasks should run')),
                Placeholder::make('info')
                    ->content(new HtmlString('
                        <div class="rounded-lg bg-info-500/10 p-4 text-sm text-info-600 dark:text-info-400">
                            <strong>'.__('What this does:').'</strong>
                            <ul class="list-disc list-inside mt-2 space-y-1">
                                <li>'.__('Creates a server cron job to run').' <code>wp-cron.php</code></li>
                                <li>'.__('Adds').' <code>define(\'DISABLE_WP_CRON\', true);</code> '.__('to your').' <code>wp-config.php</code></li>
                                <li>'.__('Improves scheduled task reliability (posts, backups, updates)').'</li>
                                <li>'.__('Reduces page load times by removing cron checks from visitors').'</li>
                            </ul>
                        </div>
                    ')),
            ])
            ->action(function (array $data): void {
                try {
                    $domain = Domain::findOrFail($data['domain_id']);
                    $username = $this->getUsername();

                    // Add DISABLE_WP_CRON to wp-config.php
                    $result = $this->getAgent()->cronWordPressSetup(
                        $username,
                        $domain->domain,
                        $data['schedule'],
                        false // enable - adds DISABLE_WP_CRON to wp-config
                    );

                    if (! $result['success']) {
                        throw new Exception($result['error'] ?? __('Failed to setup WordPress cron'));
                    }

                    // Save to database - Laravel scheduler will handle execution
                    $command = "cd /home/{$username}/domains/{$domain->domain}/public_html && /usr/bin/php wp-cron.php";

                    CronJob::updateOrCreate(
                        [
                            'user_id' => Auth::id(),
                            'type' => 'wordpress',
                            'metadata->domain_id' => $domain->id,
                        ],
                        [
                            'name' => "WordPress Cron - {$domain->domain}",
                            'schedule' => $data['schedule'],
                            'command' => $command,
                            'is_active' => true,
                            'metadata' => ['domain_id' => $domain->id, 'domain' => $domain->domain],
                        ]
                    );

                    Notification::make()
                        ->title(__('WordPress cron enabled'))
                        ->body(__('Server cron is now handling scheduled tasks for :domain', ['domain' => $domain->domain]))
                        ->success()
                        ->send();
                } catch (Exception $e) {
                    Notification::make()
                        ->title(__('Error setting up WordPress cron'))
                        ->body(SafeError::message($e))
                        ->danger()
                        ->send();
                }
            });
    }

    private function validateCronCommand(string $command): void
    {
        $normalizedCommand = strtolower(trim($command));

        // Block fork bombs
        if (str_contains($normalizedCommand, ':(){ :|:& };:')) {
            throw new Exception(__('This command contains a dangerous pattern and is not allowed.'));
        }

        // Block dangerous commands at the start
        $dangerousStartPatterns = [
            'rm -rf /',
            'mkfs',
            'dd if=',
            'chmod -r 777 /',
            'chown -r',
        ];

        foreach ($dangerousStartPatterns as $pattern) {
            if (str_starts_with($normalizedCommand, $pattern)) {
                throw new Exception(__('This command contains a dangerous pattern and is not allowed.'));
            }
        }

        // Block privilege escalation
        $blockedSubstrings = [
            'sudo ',
            'su -',
            'su root',
        ];

        foreach ($blockedSubstrings as $pattern) {
            if (str_contains($normalizedCommand, $pattern)) {
                throw new Exception(__('This command contains a dangerous pattern and is not allowed.'));
            }
        }

        // Block network abuse
        $networkPatterns = [
            'nc -l',
            'ncat -l',
        ];

        foreach ($networkPatterns as $pattern) {
            if (str_contains($normalizedCommand, $pattern)) {
                throw new Exception(__('This command contains a dangerous pattern and is not allowed.'));
            }
        }

        // Block shell downloads piped to execution
        $pipedExecutionPatterns = [
            '| bash',
            '| sh',
            '| zsh',
            '| python',
            '| perl',
            '| ruby',
        ];

        foreach ($pipedExecutionPatterns as $pattern) {
            if (str_contains($normalizedCommand, $pattern)) {
                throw new Exception(__('This command contains a dangerous pattern and is not allowed.'));
            }
        }

        // Block crypto mining binaries
        $miningPatterns = [
            'xmrig',
            'minerd',
            'cpuminer',
        ];

        foreach ($miningPatterns as $pattern) {
            if (str_contains($normalizedCommand, $pattern)) {
                throw new Exception(__('This command contains a dangerous pattern and is not allowed.'));
            }
        }
    }

    public function deleteCronJob(int $id): void
    {
        try {
            $cronJob = CronJob::where('user_id', Auth::id())->findOrFail($id);

            // If it's a WordPress cron, remove the wp-config DISABLE_WP_CRON constant
            if ($cronJob->type === 'wordpress' && isset($cronJob->metadata['domain'])) {
                $this->getAgent()->cronWordPressSetup(
                    $this->getUsername(),
                    $cronJob->metadata['domain'],
                    $cronJob->schedule,
                    true // disable - removes DISABLE_WP_CRON from wp-config
                );
            }

            // Delete from database - Laravel scheduler will stop running it
            $cronJob->delete();

            Notification::make()
                ->title(__('Cron job deleted'))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error deleting cron job'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    public function updateCronJob(CronJob $cronJob, array $data): void
    {
        try {
            if ($cronJob->user_id !== Auth::id()) {
                Notification::make()
                    ->title(__('Cron job not found'))
                    ->danger()
                    ->send();

                return;
            }

            $this->validateCronCommand($data['command']);

            // Update database - Laravel scheduler uses these values
            $cronJob->update([
                'name' => $data['name'],
                'schedule' => $data['schedule'],
                'command' => $data['command'],
            ]);

            Notification::make()
                ->title(__('Cron job updated'))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error updating cron job'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    public function toggleCronJob(int $id): void
    {
        try {
            $cronJob = CronJob::where('user_id', Auth::id())->findOrFail($id);
            $newState = ! $cronJob->is_active;

            // Update database - Laravel scheduler checks is_active
            $cronJob->update(['is_active' => $newState]);

            Notification::make()
                ->title($newState ? __('Cron job enabled') : __('Cron job disabled'))
                ->success()
                ->send();
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error toggling cron job'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    public function runCronJob(int $id): void
    {
        try {
            $cronJob = CronJob::where('user_id', Auth::id())->findOrFail($id);

            $result = $this->getAgent()->cronRun(
                $this->getUsername(),
                $cronJob->command
            );

            // Update last run info
            $cronJob->update([
                'last_run_at' => now(),
                'last_run_status' => $result['success'] ? 'success' : 'failed',
                'last_run_output' => $result['output'] ?? null,
            ]);

            if ($result['success']) {
                Notification::make()
                    ->title(__('Cron job executed'))
                    ->body(__('Command completed successfully'))
                    ->success()
                    ->send();
            } else {
                Notification::make()
                    ->title(__('Cron job failed'))
                    ->body($result['output'] ?? __('Command failed'))
                    ->warning()
                    ->send();
            }
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Error running cron job'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    protected function getHeaderActions(): array
    {
        return [
            $this->createCronJobAction(),
            $this->setupWordPressCronAction(),
        ];
    }
}
