<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\Users\Pages;

use App\Filament\Admin\Resources\Users\UserResource;
use Filament\Actions\CreateAction;
use Filament\Resources\Pages\ListRecords;
use Filament\Schemas\Components\EmbeddedTable;
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\Tabs\Tab;
use Filament\Schemas\Schema;
use Illuminate\Database\Eloquent\Builder;

class ListUsers extends ListRecords
{
    protected static string $resource = UserResource::class;

    protected function getHeaderActions(): array
    {
        return [
            CreateAction::make(),
        ];
    }

    public function getTabs(): array
    {
        return [
            'users' => Tab::make(__('Users'))
                ->modifyQueryUsing(fn (Builder $query) => $query->where('is_admin', false))
                ->icon('heroicon-o-users'),
            'admins' => Tab::make(__('Administrators'))
                ->modifyQueryUsing(fn (Builder $query) => $query->where('is_admin', true))
                ->icon('heroicon-o-shield-check'),
        ];
    }

    public function getDefaultActiveTab(): string|int|null
    {
        return 'users';
    }

    public function content(Schema $schema): Schema
    {
        $tabs = $this->getCachedTabs();

        foreach ($tabs as $tab) {
            $tab->schema([EmbeddedTable::make()]);
        }

        return $schema->components([
            Tabs::make()
                ->key('resourceTabs')
                ->livewireProperty('activeTab')
                ->contained()
                ->tabs($tabs),
        ]);
    }
}
