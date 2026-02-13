@props(['users' => [], 'path' => ''])

@if(count($users) > 0)
    <div class="space-y-2">
        @foreach($users as $user)
            <div class="flex flex-col gap-3 rounded-lg border border-gray-200 bg-gray-50 px-4 py-3 dark:border-gray-700 dark:bg-gray-800 sm:flex-row sm:items-center sm:justify-between">
                <div class="flex items-center gap-3">
                    <x-filament::icon
                        icon="heroicon-o-user"
                        :size="\Filament\Support\Enums\IconSize::Medium"
                        class="fi-color-gray fi-text-color-400"
                    />
                    <span class="fi-section-header-heading">{{ $user }}</span>
                </div>
                <x-filament::button
                    size="sm"
                    color="danger"
                    icon="heroicon-o-trash"
                    wire:click="deleteProtectedDirUser('{{ $path }}', '{{ $user }}')"
                    wire:confirm="{{ __('Are you sure you want to remove this user?') }}"
                >
                    {{ __('Remove') }}
                </x-filament::button>
            </div>
        @endforeach
    </div>
@else
    <div class="rounded-lg border border-dashed border-gray-300 bg-gray-50 p-6 text-center dark:border-gray-600 dark:bg-gray-800">
        <x-filament::icon
            icon="heroicon-o-user-group"
            :size="\Filament\Support\Enums\IconSize::TwoExtraLarge"
            class="mx-auto fi-color-gray fi-text-color-400"
        />
        <p class="mt-2 fi-section-header-description">
            {{ __('No users configured yet. Add a user below.') }}
        </p>
    </div>
@endif
