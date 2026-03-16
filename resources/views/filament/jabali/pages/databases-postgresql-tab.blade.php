<div class="space-y-4">
    {{-- Sub-tab buttons --}}
    <div class="flex gap-2">
        <x-filament::button
            :color="$this->pgSubTab === 'databases' ? 'primary' : 'gray'"
            icon="heroicon-o-circle-stack"
            wire:click="setPgSubTab('databases')"
            size="sm"
        >
            {{ __('Databases') }}
        </x-filament::button>
        <x-filament::button
            :color="$this->pgSubTab === 'users' ? 'primary' : 'gray'"
            icon="heroicon-o-users"
            wire:click="setPgSubTab('users')"
            size="sm"
        >
            {{ __('Users') }}
        </x-filament::button>
    </div>

    {{-- Table --}}
    <div>
        {{ $this->table }}
    </div>
</div>
