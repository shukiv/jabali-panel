<x-filament-panels::page>
    {{ $this->settingsForm }}

    @if ($activeTab === 'addons')
        <x-filament::section icon="heroicon-o-puzzle-piece">
            <x-slot name="heading">{{ __('Installed Addons') }}</x-slot>
            <x-slot name="description">{{ __('Extend Jabali with official addon tools. Install or remove addons from this page.') }}</x-slot>

            <div class="mb-4">
                <x-filament::button wire:click="loadAddons" icon="heroicon-o-arrow-path" color="gray" size="sm">
                    {{ __('Refresh') }}
                </x-filament::button>
            </div>

            <div class="grid grid-cols-1 gap-4 md:grid-cols-2">
                @forelse ($addonsData as $addon)
                    <div class="rounded-xl bg-white p-4 shadow-sm ring-1 ring-gray-950/5 dark:bg-gray-900 dark:ring-white/10">
                        <h3 class="text-base font-semibold text-gray-950 dark:text-white">{{ $addon['name'] }}</h3>
                        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ $addon['description'] }}</p>

                        <div class="mt-3">
                            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ __('Status') }}</span>
                            <div class="mt-1">
                                @if ($addon['installed'] ?? false)
                                    <span class="inline-flex items-center gap-x-1 rounded-md bg-success-50 px-2 py-1 text-xs font-medium text-success-700 ring-1 ring-inset ring-success-600/20 dark:bg-success-400/10 dark:text-success-400 dark:ring-success-400/20">
                                        {{ ($addon['service_active'] ?? null) === false ? __('Installed (stopped)') : __('Installed') }}
                                        @if ($addon['version'] ?? null)
                                            <span class="text-gray-400">v{{ $addon['version'] }}</span>
                                        @endif
                                    </span>
                                @else
                                    <span class="inline-flex items-center rounded-md bg-gray-50 px-2 py-1 text-xs font-medium text-gray-600 ring-1 ring-inset ring-gray-500/10 dark:bg-gray-400/10 dark:text-gray-400 dark:ring-gray-400/20">
                                        {{ __('Not Installed') }}
                                    </span>
                                @endif
                            </div>
                        </div>

                        <div class="mt-4">
                            @if ($addon['installed'] ?? false)
                                <x-filament::button
                                    wire:click="manageAddon('{{ $addon['id'] }}', 'uninstall')"
                                    wire:confirm="{{ __('This will remove :name from the server. Existing data will not be deleted. Continue?', ['name' => $addon['name']]) }}"
                                    icon="heroicon-o-trash"
                                    color="danger"
                                    size="sm"
                                >
                                    {{ __('Uninstall') }}
                                </x-filament::button>
                            @else
                                <x-filament::button
                                    wire:click="manageAddon('{{ $addon['id'] }}', 'install')"
                                    wire:confirm="{{ __('This will download and install :name on the server. Continue?', ['name' => $addon['name']]) }}"
                                    icon="heroicon-o-arrow-down-tray"
                                    color="primary"
                                    size="sm"
                                >
                                    {{ __('Install') }}
                                </x-filament::button>
                            @endif
                        </div>
                    </div>
                @empty
                    <p class="text-sm text-gray-500 dark:text-gray-400">{{ __('No addons available.') }}</p>
                @endforelse
            </div>
        </x-filament::section>
    @endif

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
