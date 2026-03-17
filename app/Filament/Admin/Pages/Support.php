<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Support\SafeError;
use BackedEnum;
use Exception;
use Filament\Actions\Action;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Contracts\View\View;
use Illuminate\Support\Facades\Artisan;

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

    public function copyReport(): void
    {
        $encoded = base64_encode($this->diagnosticReport);
        $this->js("navigator.clipboard.writeText(atob('{$encoded}'))");

        Notification::make()
            ->title(__('Copied to clipboard'))
            ->success()
            ->duration(2000)
            ->send();
    }

    public function emailReport(): void
    {
        $encoded = base64_encode($this->diagnosticReport);
        $this->js("window.location.href = 'mailto:webmaster@jabali-panel.com?subject=' + encodeURIComponent('[Jabali] Diagnostic Report') + '&body=' + encodeURIComponent(atob('{$encoded}'))");
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('diagnostic_report')
                ->label(__('Diagnostic Report'))
                ->icon('heroicon-o-document-arrow-down')
                ->color('gray')
                ->modalHeading(__('Diagnostic Report'))
                ->modalDescription(__('The report is encrypted — only the Jabali team can read it. Copy it into a GitHub issue or send it via email.'))
                ->modalSubmitAction(false)
                ->modalCancelActionLabel(__('Close'))
                ->mountUsing(function () {
                    try {
                        Artisan::call('jabali:report');
                        $this->diagnosticReport = trim(Artisan::output());
                    } catch (Exception $e) {
                        Notification::make()
                            ->title(__('Report generation failed'))
                            ->body(SafeError::message($e))
                            ->danger()
                            ->send();
                    }
                })
                ->modalContent(fn (): View => view('filament.admin.pages.support-report', [
                    'report' => $this->diagnosticReport,
                ]))
                ->extraModalFooterActions([
                    Action::make('emailReport')
                        ->label(__('Send via Email'))
                        ->icon('heroicon-o-envelope')
                        ->color('success')
                        ->action(fn () => $this->emailReport()),
                    Action::make('copyReport')
                        ->label(__('Copy to Clipboard'))
                        ->icon('heroicon-o-clipboard-document')
                        ->action(fn () => $this->copyReport()),
                ]),
        ];
    }
}
