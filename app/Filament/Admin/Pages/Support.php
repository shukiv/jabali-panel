<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Illuminate\Contracts\Support\Htmlable;

class Support extends Page
{
    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-question-mark-circle';

    protected static ?int $navigationSort = 24;

    protected static ?string $slug = 'support';

    protected string $view = 'filament.admin.pages.support';

    public string $diagnosticReport = '';

    public static function getNavigationLabel(): string
    {
        return __('Support');
    }

    public function getTitle(): string|Htmlable
    {
        return __('Support');
    }

    public function generateReport(): void
    {
        try {
            \Illuminate\Support\Facades\Artisan::call('jabali:report');
            $this->diagnosticReport = trim(\Illuminate\Support\Facades\Artisan::output());
            $this->dispatch('report-generated');
        } catch (Exception $e) {
            Notification::make()
                ->title(__('Report generation failed'))
                ->body($e->getMessage())
                ->danger()
                ->send();
        }
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('diagnostic_report')
                ->label(__('Diagnostic Report'))
                ->icon('heroicon-o-document-arrow-down')
                ->color('gray')
                ->requiresConfirmation()
                ->modalHeading(__('Generate Diagnostic Report'))
                ->modalDescription(__('This will generate an encrypted diagnostic report containing system information, service statuses, and recent logs. The report is encrypted and can only be read by the Jabali team. You can paste it in a GitHub issue.'))
                ->modalSubmitActionLabel(__('Generate'))
                ->action(fn () => $this->generateReport()),
        ];
    }
}
