<?php

declare(strict_types=1);

namespace App\JabaliSecurity\Widgets;

use App\JabaliSecurity\JabaliSecurityClient;
use App\JabaliSecurity\Pages\Security;
use Filament\Actions\Action;
use Filament\Actions\BulkAction;
use Filament\Notifications\Notification;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Support\Collection;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Livewire\Component;

class UnifiedBlocklistTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    protected function client(): JabaliSecurityClient
    {
        return JabaliSecurityClient::getInstance();
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(function () {
                try {
                    $page = request()->query('blocklist_page', 1);
                    $data = $this->client()->get('/blocklist/unified?page=' . (int) $page . '&per_page=100');
                    return $data['blocked_ips'] ?? [];
                } catch (\Exception) {
                    return [];
                }
            })
            ->columns([
                TextColumn::make('ip')
                    ->label(__('IP Address'))
                    ->searchable(),
                TextColumn::make('reason')
                    ->label(__('Reason'))
                    ->limit(50),
                TextColumn::make('source')
                    ->label(__('Source'))
                    ->badge()
                    ->color(fn (string $state): string => match ($state) {
                        'bruteforce' => 'warning',
                        'crowdsec' => 'info',
                        'threat_intel' => 'purple',
                        'manual' => 'gray',
                        default => 'gray',
                    }),
                TextColumn::make('duration')
                    ->label(__('Expires'))
                    ->state(function ($record): string {
                        $val = $record['duration'] ?? '';
                        if (! $val || $val === 'permanent') {
                            return __('Permanent');
                        }
                        // ISO timestamp (jabali blocked_ips.expires_at)
                        if (str_contains($val, 'T') || str_contains($val, '-')) {
                            try {
                                $expires = \Carbon\Carbon::parse($val);
                                if ($expires->isPast()) {
                                    return __('Expired');
                                }
                                return $expires->diffForHumans();
                            } catch (\Throwable) {
                                return $val;
                            }
                        }
                        // Go duration format from CrowdSec (e.g. "3h23m36s")
                        if (preg_match('/^-/', $val)) {
                            return __('Expired');
                        }
                        $seconds = 0;
                        if (preg_match('/(\d+)h/', $val, $m)) {
                            $seconds += (int) $m[1] * 3600;
                        }
                        if (preg_match('/(\d+)m/', $val, $m)) {
                            $seconds += (int) $m[1] * 60;
                        }
                        if ($seconds <= 0) {
                            return $val;
                        }
                        return \Carbon\Carbon::now()->addSeconds($seconds)->diffForHumans();
                    }),
            ])
            ->actions([
                Action::make('whitelist')
                    ->label(__('Whitelist'))
                    ->icon('heroicon-o-shield-check')
                    ->color('success')
                    ->requiresConfirmation()
                    ->modalDescription(__('Unblock and add to whitelist so this IP is never blocked again.'))
                    ->action(function ($record): void {
                        $ip = $record['ip'] ?? '';
                        $source = $record['source'] ?? '';
                        if (! $ip) {
                            return;
                        }
                        // Remove from both jabali and CrowdSec
                        $this->client()->delete('/block/' . urlencode($ip));
                        if ($source === 'crowdsec') {
                            $this->client()->delete('/crowdsec/decisions/' . urlencode($ip));
                        }
                        $this->client()->post('/bruteforce/whitelist', ['ip' => $ip]);
                        Notification::make()->title(__('IP whitelisted: :ip', ['ip' => $ip]))->success()->send();
                        $this->redirect(Security::tabUrl('defense', 'bruteforce'), navigate: true);
                    }),
                Action::make('unblock')
                    ->label(__('Unblock'))
                    ->icon('heroicon-o-lock-open')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->action(function ($record): void {
                        $ip = $record['ip'] ?? '';
                        $source = $record['source'] ?? '';
                        if (! $ip) {
                            return;
                        }
                        // Remove from the appropriate source
                        if ($source === 'crowdsec') {
                            $this->client()->delete('/crowdsec/decisions/' . urlencode($ip));
                        } else {
                            $this->client()->delete('/block/' . urlencode($ip));
                        }
                        Notification::make()->title(__('IP unblocked: :ip', ['ip' => $ip]))->success()->send();
                        $this->redirect(Security::tabUrl('defense', 'bruteforce'), navigate: true);
                    }),
            ])
            ->emptyStateHeading(__('No blocked IPs'))
            ->emptyStateDescription(__('Blocked IPs from brute-force detection, CrowdSec, threat intelligence, and manual blocks will appear here'))
            ->striped();
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
