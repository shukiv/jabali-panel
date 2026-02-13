<div class="mt-4">
    {{ $this->spamForm }}

    <div class="mt-6">
        <x-filament::button wire:click="saveSpamSettings" icon="heroicon-o-check" color="primary">
            {{ __('Save Spam Settings') }}
        </x-filament::button>
    </div>
</div>
