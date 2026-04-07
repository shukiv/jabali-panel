<x-filament-panels::page>
    {{ $this->settingsForm }}

    @if ($activeTab === 'general')
        <x-filament::section icon="heroicon-o-paint-brush">
            <x-slot name="heading">{{ __('Panel Branding') }}</x-slot>

            <div class="space-y-6">
                <div>
                    <label class="block text-sm font-medium text-gray-700 dark:text-gray-300">
                        {{ __('Control Panel Name') }}
                    </label>
                    <input
                        type="text"
                        wire:model="brandingData.panel_name"
                        placeholder="{{ __('Jabali') }}"
                        class="fi-input mt-1 block w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-950 shadow-sm transition duration-75 focus:border-primary-500 focus:ring-1 focus:ring-primary-500 dark:border-gray-700 dark:bg-gray-900 dark:text-white"
                    >
                    <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ __('Appears in browser title and navigation') }}</p>
                </div>

                <div class="grid grid-cols-1 gap-6 md:grid-cols-2">
                    {{-- Light Logo --}}
                    <div class="flex gap-4 rounded-xl p-4 ring-1 ring-gray-950/5 dark:ring-white/10">
                        <div class="flex h-20 w-20 shrink-0 items-center justify-center rounded-lg bg-white ring-1 ring-gray-200">
                            @if ($this->currentLogo)
                                <img src="/storage/{{ $this->currentLogo }}" alt="{{ __('Light Logo') }}" class="max-h-16 max-w-16 object-contain">
                            @else
                                <x-heroicon-o-photo class="h-8 w-8 text-gray-300" />
                            @endif
                        </div>
                        <div class="flex flex-col justify-center gap-2">
                            <p class="text-sm font-medium text-gray-950 dark:text-white">{{ __('Light Logo') }}</p>
                            <div class="flex items-center gap-2">
                                <input type="file" id="logoLightUpload" wire:model="logoLightUpload" accept="image/png,image/jpeg,image/webp,image/svg+xml" class="hidden">
                                <label for="logoLightUpload" class="fi-btn fi-btn-size-sm fi-color-custom fi-color-gray inline-flex cursor-pointer items-center justify-center gap-1.5 rounded-lg px-3 py-1.5 text-sm font-semibold shadow-sm ring-1 ring-gray-950/10 transition-colors hover:bg-gray-50 dark:ring-white/20 dark:hover:bg-white/5">
                                    <x-heroicon-m-arrow-up-tray class="h-4 w-4" />
                                    {{ __('Choose File') }}
                                </label>
                                <span wire:loading wire:target="logoLightUpload" class="text-xs text-primary-600">{{ __('Uploading...') }}</span>
                                @if ($logoLightUpload)
                                    <x-filament::button wire:click="saveLogoLight" icon="heroicon-o-check" size="sm" color="success">
                                        {{ __('Save') }}
                                    </x-filament::button>
                                @endif
                            </div>
                            @error('logoLightUpload') <p class="text-xs text-danger-600">{{ $message }}</p> @enderror
                        </div>
                    </div>

                    {{-- Dark Logo --}}
                    <div class="flex gap-4 rounded-xl p-4 ring-1 ring-gray-950/5 dark:ring-white/10">
                        <div class="flex h-20 w-20 shrink-0 items-center justify-center rounded-lg bg-gray-950 ring-1 ring-gray-700">
                            @if ($this->currentLogoDark)
                                <img src="/storage/{{ $this->currentLogoDark }}" alt="{{ __('Dark Logo') }}" class="max-h-16 max-w-16 object-contain">
                            @else
                                <x-heroicon-o-photo class="h-8 w-8 text-gray-600" />
                            @endif
                        </div>
                        <div class="flex flex-col justify-center gap-2">
                            <p class="text-sm font-medium text-gray-950 dark:text-white">{{ __('Dark Logo') }}</p>
                            <div class="flex items-center gap-2">
                                <input type="file" id="logoDarkUpload" wire:model="logoDarkUpload" accept="image/png,image/jpeg,image/webp,image/svg+xml" class="hidden">
                                <label for="logoDarkUpload" class="fi-btn fi-btn-size-sm fi-color-custom fi-color-gray inline-flex cursor-pointer items-center justify-center gap-1.5 rounded-lg px-3 py-1.5 text-sm font-semibold shadow-sm ring-1 ring-gray-950/10 transition-colors hover:bg-gray-50 dark:ring-white/20 dark:hover:bg-white/5">
                                    <x-heroicon-m-arrow-up-tray class="h-4 w-4" />
                                    {{ __('Choose File') }}
                                </label>
                                <span wire:loading wire:target="logoDarkUpload" class="text-xs text-primary-600">{{ __('Uploading...') }}</span>
                                @if ($logoDarkUpload)
                                    <x-filament::button wire:click="saveLogoDark" icon="heroicon-o-check" size="sm" color="success">
                                        {{ __('Save') }}
                                    </x-filament::button>
                                @endif
                            </div>
                            @error('logoDarkUpload') <p class="text-xs text-danger-600">{{ $message }}</p> @enderror
                        </div>
                    </div>
                </div>

                <div class="flex gap-3">
                    <x-filament::button wire:click="saveBranding">
                        {{ __('Save Branding') }}
                    </x-filament::button>
                    @if ($this->currentLogo || $this->currentLogoDark)
                        <x-filament::button wire:click="removeLogo" color="danger" icon="heroicon-o-trash" wire:confirm="{{ __('Are you sure you want to remove the logos?') }}">
                            {{ __('Remove Logos') }}
                        </x-filament::button>
                    @endif
                </div>
            </div>
        </x-filament::section>
    @endif

    <x-filament-actions::modals />
</x-filament-panels::page>
