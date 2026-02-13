<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use BackedEnum;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\CheckboxList;
use Filament\Forms\Components\TextInput;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Support\Icons\Heroicon;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Auth;

class AutomationApi extends Page implements HasActions, HasTable
{
    use InteractsWithActions;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedKey;

    protected static ?int $navigationSort = 17;

    protected static ?string $slug = 'automation-api';

    protected string $view = 'filament.admin.pages.automation-api';

    public array $tokens = [];

    public ?string $plainToken = null;

    public function getTitle(): string|Htmlable
    {
        return __('API for Automation');
    }

    public static function getNavigationLabel(): string
    {
        return __('API Access');
    }

    public function mount(): void
    {
        $this->loadTokens();
    }

    protected function loadTokens(): void
    {
        $user = Auth::user();
        if (! $user) {
            $this->tokens = [];

            return;
        }

        $this->tokens = $user->tokens()
            ->orderByDesc('created_at')
            ->get()
            ->map(function ($token) {
                return [
                    'id' => $token->id,
                    'name' => $token->name,
                    'abilities' => implode(', ', $token->abilities ?? []),
                    'last_used_at' => $token->last_used_at?->format('Y-m-d H:i') ?? __('Never'),
                    'created_at' => $token->created_at?->format('Y-m-d H:i') ?? '',
                ];
            })
            ->toArray();

        $this->resetTable();
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->tokens)
            ->columns([
                TextColumn::make('name')
                    ->label(__('Token')),
                TextColumn::make('abilities')
                    ->label(__('Abilities'))
                    ->wrap(),
                TextColumn::make('last_used_at')
                    ->label(__('Last Used')),
                TextColumn::make('created_at')
                    ->label(__('Created')),
            ])
            ->recordActions([
                Action::make('revoke')
                    ->label(__('Revoke'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->action(function (array $record): void {
                        $user = Auth::user();
                        if (! $user) {
                            return;
                        }
                        $user->tokens()->where('id', $record['id'])->delete();
                        Notification::make()->title(__('Token revoked'))->success()->send();
                        $this->loadTokens();
                    }),
            ])
            ->headerActions([
                Action::make('createToken')
                    ->label(__('Create Token'))
                    ->icon('heroicon-o-plus-circle')
                    ->color('primary')
                    ->modalHeading(__('Create API Token'))
                    ->form([
                        TextInput::make('name')
                            ->label(__('Token Name'))
                            ->required(),
                        CheckboxList::make('abilities')
                            ->label(__('Abilities'))
                            ->options([
                                'automation' => __('Automation API'),
                                'read' => __('Read Only'),
                                'write' => __('Write'),
                            ])
                            ->columns(2)
                            ->default(['automation']),
                    ])
                    ->action(function (array $data): void {
                        $user = Auth::user();
                        if (! $user) {
                            return;
                        }

                        $abilities = $data['abilities'] ?? ['automation'];
                        if (empty($abilities)) {
                            $abilities = ['automation'];
                        }

                        $token = $user->createToken($data['name'], $abilities);
                        $this->plainToken = $token->plainTextToken;
                        Notification::make()->title(__('Token created'))->success()->send();
                        $this->loadTokens();
                    }),
            ])
            ->emptyStateHeading(__('No API tokens yet'))
            ->emptyStateDescription(__('Create a token to access the automation API.'));
    }
}
