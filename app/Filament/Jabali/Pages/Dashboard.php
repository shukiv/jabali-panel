<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Filament\Jabali\Widgets\DiskUsageWidget;
use App\Filament\Jabali\Widgets\DomainsWidget;
use App\Filament\Jabali\Widgets\MailboxesWidget;
use App\Filament\Jabali\Widgets\RecentBackupsWidget;
use App\Filament\Jabali\Widgets\StatsOverview;
use App\Models\DnsSetting;
use BackedEnum;
use Filament\Actions\Action;
use Filament\Pages\Dashboard as BaseDashboard;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Text;
use Illuminate\Support\Facades\Auth;

class Dashboard extends BaseDashboard
{
    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-home';

    protected static ?int $navigationSort = 1;

    public function getWidgets(): array
    {
        return [
            StatsOverview::class,
            DiskUsageWidget::class,
            DomainsWidget::class,
            MailboxesWidget::class,
            RecentBackupsWidget::class,
        ];
    }

    public function getColumns(): int|array
    {
        return [
            'default' => 1,
            'sm' => 1,
            'md' => 2,
            'lg' => 2,
            'xl' => 2,
        ];
    }

    public function mount(): void
    {
        if (! DnsSetting::get('user_onboarding_completed_'.(string) Auth::id(), false)) {
            $this->defaultAction = 'onboarding';
        }
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('onboarding')->modalCancelActionLabel(__('Maybe later'))
                ->label(__('Setup Wizard'))
                ->icon('heroicon-o-sparkles')

                ->modalHeading(__('Welcome to Jabali!'))
                ->modalDescription(__('Here is a quick path to launch your first site.'))
                ->modalWidth('2xl')
                ->form([
                    Section::make(__('Next Steps'))
                        ->description(__('Follow these steps to get online quickly.'))
                        ->icon('heroicon-o-check-circle')
                        ->iconColor('info')
                        ->collapsed(false)
                        ->collapsible(false)
                        ->compact()
                        ->schema([
                            Grid::make(['default' => 1, 'md' => 2])
                                ->schema([
                                    Section::make(__('1. Add a Domain'))
                                        ->description(__('Point your DNS to this server or update nameservers.'))
                                        ->icon('heroicon-o-globe-alt')
                                        ->iconColor('info')
                                        ->collapsed(false)
                                        ->collapsible(false)
                                        ->compact(),
                                    Section::make(__('2. Issue SSL'))
                                        ->description(__('Enable HTTPS for your site with SSL certificates.'))
                                        ->icon('heroicon-o-lock-closed')
                                        ->iconColor('info')
                                        ->collapsed(false)
                                        ->collapsible(false)
                                        ->compact(),
                                    Section::make(__('3. Upload or Install'))
                                        ->description(__('Upload files or install WordPress to deploy your site.'))
                                        ->icon('heroicon-o-arrow-up-tray')
                                        ->iconColor('info')
                                        ->collapsed(false)
                                        ->collapsible(false)
                                        ->compact(),
                                    Section::make(__('4. Create Email & Databases'))
                                        ->description(__('Set up mailboxes and databases for your app.'))
                                        ->icon('heroicon-o-envelope')
                                        ->iconColor('info')
                                        ->collapsed(false)
                                        ->collapsible(false)
                                        ->compact(),
                                ]),
                            Text::make(__('Optional: configure backups, cron jobs, and SSH keys for day-to-day operations.'))
                                ->color('gray'),
                        ]),
                ])
                ->modalSubmitActionLabel(__("Don't show again"))
                ->action(function (): void {
                    DnsSetting::set('user_onboarding_completed_'.(string) Auth::id(), '1');
                    DnsSetting::clearCache();
                }),
        ];
    }

    public function getSubheading(): ?string
    {
        $user = Auth::user();

        return __('Welcome back, :name! Here is an overview of your hosting account.', [
            'name' => $user->name,
        ]);
    }

    public static function getNavigationLabel(): string
    {
        return __('Dashboard');
    }

    public function getTitle(): string
    {
        return __('Dashboard');
    }
}
