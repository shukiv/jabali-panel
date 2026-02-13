<x-filament-panels::page.simple>
    <form wire:submit="authenticate" class="space-y-4">
        {{ $this->form }}

        <x-filament::button
            type="submit"
            class="w-full"
        >
            {{ __('Verify') }}
        </x-filament::button>
    </form>

    <div class="text-center mt-4">
        <x-filament::link
            tag="button"
            wire:click="toggleRecoveryCode"
            color="gray"
            size="sm"
        >
            {{ $this->useRecoveryCode ? __('Use authentication code') : __('Use a recovery code') }}
        </x-filament::link>
    </div>
</x-filament-panels::page.simple>
