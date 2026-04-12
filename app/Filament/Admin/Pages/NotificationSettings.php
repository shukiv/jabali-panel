<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Models\NotificationChannel;
use App\Models\NotificationTypeChannel;
use Filament\Actions\Action;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Illuminate\Support\Collection;
use UnitEnum;

/**
 * Unified notification routing — the "one page to manage it all" answer to
 * GitHub issue #102. Lists all configured channels, exposes the severity
 * routing matrix (info / warning / critical x channels), and links out to
 * the Notification Channel resource for adding/editing drivers.
 *
 * Does not itself CRUD channels — that lives in NotificationChannelResource,
 * which gives us the full Filament table/form goodness for free.
 */
class NotificationSettings extends Page
{
    protected string $view = 'filament.admin.pages.notification-settings';

    protected static ?string $title = 'Notifications';

    protected static string|UnitEnum|null $navigationGroup = 'Server';

    protected static ?string $navigationLabel = 'Notifications';

    protected static ?int $navigationSort = 45;

    /**
     * @var array<string, array<int, int>>
     */
    public array $routing = [
        'info' => [],
        'warning' => [],
        'critical' => [],
    ];

    public function mount(): void
    {
        foreach (NotificationTypeChannel::SEVERITIES as $severity) {
            $this->routing[$severity] = NotificationTypeChannel::query()
                ->where('severity', $severity)
                ->pluck('channel_id')
                ->map(fn ($id) => (int) $id)
                ->all();
        }
    }

    /**
     * @return Collection<int, NotificationChannel>
     */
    public function getChannels(): Collection
    {
        return NotificationChannel::query()->orderBy('type')->orderBy('name')->get();
    }

    /**
     * @return array<string, string>
     */
    public function getSeverityLabels(): array
    {
        return [
            'info' => 'Info — routine events',
            'warning' => 'Warning — needs attention',
            'critical' => 'Critical — act now',
        ];
    }

    public function save(): void
    {
        foreach (NotificationTypeChannel::SEVERITIES as $severity) {
            $wanted = array_map('intval', $this->routing[$severity] ?? []);

            $existing = NotificationTypeChannel::query()
                ->where('severity', $severity)
                ->pluck('channel_id')
                ->map(fn ($id) => (int) $id)
                ->all();

            // Add rows for newly-checked channels.
            foreach (array_diff($wanted, $existing) as $channelId) {
                NotificationTypeChannel::query()->firstOrCreate([
                    'severity' => $severity,
                    'channel_id' => $channelId,
                ], ['priority' => 0]);
            }

            // Delete rows for unchecked channels.
            $toRemove = array_diff($existing, $wanted);
            if ($toRemove !== []) {
                NotificationTypeChannel::query()
                    ->where('severity', $severity)
                    ->whereIn('channel_id', $toRemove)
                    ->delete();
            }
        }

        Notification::make()
            ->title('Notification routing saved')
            ->success()
            ->send();
    }

    /**
     * @return array<int, Action>
     */
    protected function getHeaderActions(): array
    {
        return [
            Action::make('manage_channels')
                ->label('Manage channels')
                ->icon('heroicon-o-queue-list')
                ->url(fn () => route('filament.admin.resources.notification-channels.index')),

            Action::make('save')
                ->label('Save routing')
                ->icon('heroicon-o-check')
                ->color('primary')
                ->action('save'),
        ];
    }

    public static function canAccess(): bool
    {
        return auth('admin')->user()?->is_admin ?? false;
    }
}
