<?php

declare(strict_types=1);

namespace App\JabaliSecurity\Widgets;

use App\JabaliSecurity\JabaliSecurityClient;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Notifications\Notification;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Support\Collection;
use Livewire\Component;

class ScanUsersTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    /** @var array<string, array{status: string, files: int, threats_count: int, threats: array}> */
    public array $scanJobs = [];

    public bool $scanning = false;

    public bool $showResults = false;

    protected function client(): JabaliSecurityClient
    {
        return JabaliSecurityClient::getInstance();
    }

    protected function getHostingUsers(): array
    {
        return \App\Models\User::query()
            ->select('id', 'name', 'username')
            ->whereNotNull('username')
            ->where('username', '!=', '')
            ->pluck('username')
            ->all();
    }

    protected function scanPath(string $username): array
    {
        $path = '/home/' . $username;
        $result = $this->client()->post('/scan', ['path' => $path]);

        $threats = [];
        if ($result && ! empty($result['results'])) {
            foreach ($result['results'] as $r) {
                $threats[] = [
                    'path' => $r['path'] ?? '',
                    'score' => $r['score'] ?? 0,
                    'action' => $r['action'] ?? '',
                    'findings' => $r['findings'] ?? [],
                ];
            }
        }

        return [
            'success' => $result !== null,
            'files' => $result['files_scanned'] ?? 0,
            'threats_count' => $result['threats_found'] ?? $result['score'] ?? 0,
            'threats' => $threats,
        ];
    }

    public function startScanAll(): void
    {
        $users = $this->getHostingUsers();
        $this->scanJobs = [];
        $this->showResults = false;

        foreach ($users as $username) {
            $this->scanJobs[$username] = [
                'status' => 'pending',
                'files' => 0,
                'threats_count' => 0,
                'threats' => [],
            ];
        }

        $this->scanning = true;
    }

    public function startScanUsers(array $usernames): void
    {
        $this->scanJobs = [];
        $this->showResults = false;

        foreach ($usernames as $username) {
            $this->scanJobs[$username] = [
                'status' => 'pending',
                'files' => 0,
                'threats_count' => 0,
                'threats' => [],
            ];
        }

        $this->scanning = true;
    }

    public function processNextScan(): void
    {
        if (! $this->scanning) {
            return;
        }

        // Find next pending user
        $next = null;
        foreach ($this->scanJobs as $username => $job) {
            if ($job['status'] === 'pending') {
                $next = $username;
                break;
            }
        }

        if ($next === null) {
            $this->scanning = false;
            $this->showResults = true;

            return;
        }

        // Mark as scanning
        $this->scanJobs[$next]['status'] = 'scanning';

        // Execute scan (this is the blocking HTTP call — one per poll tick)
        $result = $this->scanPath($next);

        $this->scanJobs[$next]['status'] = $result['success'] ? 'done' : 'failed';
        $this->scanJobs[$next]['files'] = $result['files'];
        $this->scanJobs[$next]['threats_count'] = $result['threats_count'];
        $this->scanJobs[$next]['threats'] = $result['threats'];
    }

    public function cancelScan(): void
    {
        $this->scanning = false;
        $this->scanJobs = [];
        $this->showResults = false;
    }

    public function dismissResults(): void
    {
        $this->showResults = false;
        $this->scanJobs = [];
    }

    public function getScanProgressProperty(): string
    {
        $total = count($this->scanJobs);
        $done = 0;
        foreach ($this->scanJobs as $job) {
            if (in_array($job['status'], ['done', 'failed'], true)) {
                $done++;
            }
        }

        return "{$done}/{$total}";
    }

    public function getScanPercentProperty(): int
    {
        $total = count($this->scanJobs);
        if ($total === 0) {
            return 0;
        }
        $done = 0;
        foreach ($this->scanJobs as $job) {
            if (in_array($job['status'], ['done', 'failed'], true)) {
                $done++;
            }
        }

        return (int) round(($done / $total) * 100);
    }

    public function getTotalResultsProperty(): array
    {
        $files = 0;
        $threats = 0;
        $allThreats = [];
        $users = 0;

        foreach ($this->scanJobs as $job) {
            if ($job['status'] === 'done') {
                $users++;
                $files += $job['files'];
                $threats += $job['threats_count'];
                $allThreats = array_merge($allThreats, $job['threats']);
            }
        }

        return [
            'users' => $users,
            'files' => $files,
            'threats' => $threats,
            'all_threats' => $allThreats,
        ];
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(function () {
                try {
                    $panelUsers = \App\Models\User::query()
                        ->select('id', 'name', 'username')
                        ->whereNotNull('username')
                        ->where('username', '!=', '')
                        ->get();

                    $incidentUsers = $this->client()->get('/users') ?? [];
                    $incidentMap = [];
                    foreach ($incidentUsers as $u) {
                        $incidentMap[$u['username'] ?? ''] = $u;
                    }

                    $records = [];
                    foreach ($panelUsers as $user) {
                        $username = $user->username;
                        $incidents = $incidentMap[$username] ?? [];
                        $scanJob = $this->scanJobs[$username] ?? null;

                        $records[] = [
                            'username' => $username,
                            'incident_count' => $incidents['incident_count'] ?? 0,
                            'max_score' => $incidents['max_score'] ?? 0,
                            'quarantine_count' => $incidents['quarantine_count'] ?? 0,
                            'path' => '/home/' . $username,
                            'scan_status' => $scanJob['status'] ?? null,
                            'scan_files' => $scanJob['files'] ?? 0,
                            'scan_threats' => $scanJob['threats_count'] ?? 0,
                        ];
                    }

                    $panelUsernames = $panelUsers->pluck('username')->all();
                    foreach ($incidentUsers as $u) {
                        $username = $u['username'] ?? '';
                        if (! $username || in_array($username, $panelUsernames, true)) {
                            continue;
                        }
                        $scanJob = $this->scanJobs[$username] ?? null;
                        $records[] = [
                            'username' => $username,
                            'incident_count' => $u['incident_count'] ?? 0,
                            'max_score' => $u['max_score'] ?? 0,
                            'quarantine_count' => $u['quarantine_count'] ?? 0,
                            'path' => '/home/' . $username,
                            'scan_status' => $scanJob['status'] ?? null,
                            'scan_files' => $scanJob['files'] ?? 0,
                            'scan_threats' => $scanJob['threats_count'] ?? 0,
                        ];
                    }

                    return $records;
                } catch (\Exception) {
                    return [];
                }
            })
            ->columns([
                TextColumn::make('username')
                    ->label(__('User'))
                    ->weight('bold')
                    ->searchable(),
                TextColumn::make('path')
                    ->label(__('Path'))
                    ->fontFamily('mono')
                    ->size('sm')
                    ->color('gray'),
                TextColumn::make('scan_status')
                    ->label(__('Status'))
                    ->badge()
                    ->formatStateUsing(fn ($state): string => match ($state) {
                        'pending' => __('Pending'),
                        'scanning' => __('Scanning...'),
                        'done' => __('Done'),
                        'failed' => __('Failed'),
                        default => '',
                    })
                    ->color(fn ($state): string => match ($state) {
                        'pending' => 'gray',
                        'scanning' => 'info',
                        'done' => 'success',
                        'failed' => 'danger',
                        default => 'gray',
                    })
                    ->icon(fn ($state): ?string => match ($state) {
                        'scanning' => 'heroicon-o-arrow-path',
                        'done' => 'heroicon-o-check-circle',
                        'failed' => 'heroicon-o-x-circle',
                        default => null,
                    })
                    ->visible(fn (): bool => $this->scanning || $this->showResults),
                TextColumn::make('incident_count')
                    ->label(__('Incidents'))
                    ->badge()
                    ->color(fn ($state): string => (int) $state > 0 ? 'danger' : 'gray'),
                TextColumn::make('max_score')
                    ->label(__('Max Score'))
                    ->badge()
                    ->color(fn ($state): string => match (true) {
                        (int) $state >= 70 => 'danger',
                        (int) $state >= 40 => 'warning',
                        default => 'gray',
                    }),
                TextColumn::make('quarantine_count')
                    ->label(__('Quarantined'))
                    ->badge()
                    ->color(fn ($state): string => (int) $state > 0 ? 'warning' : 'gray'),
            ])
            ->recordActions([
                Action::make('scan')
                    ->label(__('Scan'))
                    ->icon('heroicon-o-magnifying-glass')
                    ->color('warning')
                    ->requiresConfirmation()
                    ->hidden(fn (): bool => $this->scanning)
                    ->action(function (array $record): void {
                        $username = $record['username'] ?? '';
                        $this->startScanUsers([$username]);
                    }),
            ])
            ->headerActions([
                Action::make('scanAll')
                    ->label(__('Scan All Users'))
                    ->icon('heroicon-o-shield-exclamation')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalDescription(__('Each user will be scanned individually. You will see live progress as each scan completes.'))
                    ->hidden(fn (): bool => $this->scanning)
                    ->action(fn () => $this->startScanAll()),
                Action::make('cancelScan')
                    ->label(__('Cancel'))
                    ->icon('heroicon-o-x-mark')
                    ->color('gray')
                    ->visible(fn (): bool => $this->scanning)
                    ->action(fn () => $this->cancelScan()),
            ])
            ->bulkActions([
                \Filament\Actions\BulkAction::make('scanSelected')
                    ->label(__('Scan Selected'))
                    ->icon('heroicon-o-magnifying-glass')
                    ->color('warning')
                    ->requiresConfirmation()
                    ->hidden(fn (): bool => $this->scanning)
                    ->deselectRecordsAfterCompletion()
                    ->action(function (Collection $records): void {
                        $usernames = $records->pluck('username')->filter()->values()->all();
                        $this->startScanUsers($usernames);
                    }),
            ])
            ->emptyStateHeading(__('No users found'))
            ->emptyStateDescription(__('No hosting users detected on this server'))
            ->emptyStateIcon('heroicon-o-users')
            ->striped();
    }

    public function render()
    {
        return view('jabali-security::scan-users-table');
    }
}
