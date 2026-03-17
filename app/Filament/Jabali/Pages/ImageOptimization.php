<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Models\Domain;
use App\Services\Agent\AgentClient;
use App\Support\SafeError;
use BackedEnum;
use Exception;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Schema;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Auth;

class ImageOptimization extends Page implements HasActions, HasForms
{
    use InteractsWithActions;
    use InteractsWithForms;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-photo';

    protected static ?int $navigationSort = 21;

    protected static ?string $slug = 'image-optimization';

    protected string $view = 'filament.jabali.pages.image-optimization';

    /**
     * Livewire v4 + Filament form fields entangle to nested state paths.
     * Ensure keys exist to avoid "property cannot be found" client-side errors.
     *
     * @var array{domain_id: int|null, path: string|null, convert_webp: bool, quality: int}
     */
    public array $imageFormData = [
        'domain_id' => null,
        'path' => null,
        'convert_webp' => false,
        'quality' => 82,
    ];

    public function mount(): void
    {
        // Seed the Filament form state so Alpine/Livewire entanglement has concrete keys.
        $this->imageForm->fill($this->imageFormData);
    }

    public function getTitle(): string|Htmlable
    {
        return __('Image Optimization');
    }

    public static function getNavigationLabel(): string
    {
        return __('Image Optimization');
    }

    protected function getAgent(): AgentClient
    {
        return app(AgentClient::class);
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

    protected function getForms(): array
    {
        return ['imageForm'];
    }

    public function imageForm(Schema $schema): Schema
    {
        return $schema
            ->statePath('imageFormData')
            ->schema([
                Section::make(__('Optimization Settings'))
                    ->schema([
                        Select::make('domain_id')
                            ->label(__('Domain'))
                            ->options($this->getDomainOptions())
                            ->searchable()
                            ->required(),
                        TextInput::make('path')
                            ->label(__('Custom Path (optional)'))
                            ->helperText(__('Leave empty to optimize the selected domain root')),
                        Toggle::make('convert_webp')
                            ->label(__('Generate WebP copies'))
                            ->default(false),
                        TextInput::make('quality')
                            ->label(__('Quality (40-95)'))
                            ->numeric()
                            ->default(82),
                    ])
                    ->columns(2),
            ]);
    }

    public function runOptimization(): void
    {
        $data = $this->imageForm->getState();
        $domainId = $data['domain_id'] ?? null;

        if (! $domainId) {
            Notification::make()->title(__('Select a domain'))->danger()->send();

            return;
        }

        $domain = Domain::where('id', $domainId)->where('user_id', Auth::id())->first();
        if (! $domain) {
            Notification::make()->title(__('Domain not found'))->danger()->send();

            return;
        }

        $path = trim((string) ($data['path'] ?? ''));
        if ($path === '') {
            $path = $domain->document_root;
        }

        try {
            $result = $this->getAgent()->imageOptimize(
                $this->getUsername(),
                $path,
                (bool) ($data['convert_webp'] ?? false),
                (int) ($data['quality'] ?? 82)
            );

            if ($result['success'] ?? false) {
                $optimized = $result['optimized'] ?? [];
                $message = __('Optimization complete');
                if (! empty($optimized)) {
                    $message .= ' · '.__('JPG').': '.($optimized['jpg'] ?? 0).', '.__('PNG').': '.($optimized['png'] ?? 0).', '.__('WebP').': '.($optimized['webp'] ?? 0);
                }

                Notification::make()->title($message)->success()->send();

                return;
            }

            Notification::make()->title(__('Optimization failed'))->body($result['error'] ?? '')->danger()->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Optimization failed'))->body(SafeError::message($e))->danger()->send();
        }
    }
}
