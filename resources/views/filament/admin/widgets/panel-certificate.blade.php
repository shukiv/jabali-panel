<x-filament-widgets::widget>
    @php $cert = $this->getCertData(); @endphp
    <x-filament::section>
        <x-slot name="heading">
            {{ __('Panel Certificate') }}
        </x-slot>
        <x-slot name="headerEnd">
            <x-filament::badge :color="$cert['status_color']">
                {{ $cert['status_label'] }}
            </x-filament::badge>
        </x-slot>

        <div class="grid grid-cols-2 gap-4 sm:grid-cols-4">
            <div>
                <p class="fi-section-header-description">{{ __('Hostname') }}</p>
                <p class="text-sm font-semibold text-gray-950 dark:text-white">{{ $cert['hostname'] }}</p>
            </div>

            <div>
                <p class="fi-section-header-description">{{ __('Issuer') }}</p>
                <p class="text-sm font-semibold text-gray-950 dark:text-white">{{ $cert['issuer'] }}</p>
            </div>

            <div>
                <p class="fi-section-header-description">{{ __('Expires') }}</p>
                <p class="text-sm font-semibold text-gray-950 dark:text-white">
                    @if($cert['expires_at'])
                        {{ $cert['expires_at'] }}
                        @if($cert['days_remaining'] !== null)
                            <x-filament::badge
                                size="sm"
                                :color="$cert['days_remaining'] <= 7 ? 'danger' : ($cert['days_remaining'] <= 30 ? 'warning' : 'success')"
                            >{{ $cert['days_remaining'] }}d</x-filament::badge>
                        @endif
                    @else
                        -
                    @endif
                </p>
            </div>

            <div>
                <p class="fi-section-header-description">{{ __('Last Renewal') }}</p>
                <p class="text-sm font-semibold text-gray-950 dark:text-white">
                    @if($cert['last_renewal_at'])
                        {{ $cert['last_renewal_at'] }}
                        <x-filament::badge
                            size="sm"
                            :color="$cert['last_renewal_result'] === 'success' ? 'success' : 'danger'"
                        >{{ $cert['last_renewal_result'] === 'success' ? __('OK') : __('Failed') }}</x-filament::badge>
                    @else
                        -
                    @endif
                </p>
            </div>
        </div>

        @if($cert['last_error'])
            <x-filament::section compact class="mt-3">
                <p class="text-sm text-danger-600 dark:text-danger-400">{{ $cert['last_error'] }}</p>
            </x-filament::section>
        @endif

        @if($cert['is_self_signed'] || $cert['status'] === 'pending')
            <div class="mt-3 flex items-center justify-between rounded-xl bg-warning-50 p-3 dark:bg-warning-950/20 ring-1 ring-warning-200 dark:ring-warning-800">
                <p class="text-sm text-warning-700 dark:text-warning-400">
                    {{ __('Using self-signed certificate. Click "Issue Certificate" to request a Let\'s Encrypt certificate.') }}
                </p>
                <x-filament::button
                    size="sm"
                    wire:click="issuePanelCert"
                    wire:loading.attr="disabled"
                    icon="heroicon-m-shield-check"
                >
                    {{ __('Issue Certificate') }}
                </x-filament::button>
            </div>
        @endif
    </x-filament::section>
</x-filament-widgets::widget>
