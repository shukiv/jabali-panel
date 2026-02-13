<x-filament-widgets::widget>
    <x-filament::section>
        <x-slot name="heading">
            <div class="flex flex-wrap items-center gap-2">
                @php $data = $this->getData(); @endphp
                <span>{{ __('Top Processes') }}</span>
                <x-filament::badge color="gray">{{ $data['total'] ?? 0 }} {{ __('total') }}</x-filament::badge>
                <x-filament::badge color="success">{{ $data['running'] ?? 0 }} {{ __('running') }}</x-filament::badge>
            </div>
        </x-slot>

        <div class="space-y-2">
            @forelse(($data['top'] ?? []) as $index => $proc)
                @php
                    $cpuColor = $proc['cpu'] > 50 ? 'danger' : ($proc['cpu'] > 20 ? 'warning' : 'success');
                    $memColor = $proc['memory'] > 50 ? 'danger' : ($proc['memory'] > 20 ? 'warning' : 'gray');
                @endphp

                <div class="flex flex-col gap-3 rounded-lg border border-gray-200 p-3 transition-colors hover:bg-gray-50 dark:border-white/10 dark:hover:bg-white/5 sm:flex-row sm:items-center sm:gap-4">
                    {{-- Rank --}}
                <div class="flex h-8 w-8 items-center justify-center rounded-full bg-gray-100 dark:bg-white/10">
                    <span class="fi-section-header-heading">{{ $index + 1 }}</span>
                </div>

                    {{-- Process info --}}
                    <div class="flex-1 min-w-0">
                        <div class="flex items-center gap-2">
                            <span class="fi-section-header-heading truncate">{{ Str::limit($proc['command'], 35) }}</span>
                            <x-filament::badge color="gray" size="sm">PID {{ $proc['pid'] }}</x-filament::badge>
                        </div>
                        <div class="fi-section-header-description">
                            <span>{{ __('User') }}: {{ $proc['user'] }}</span>
                        </div>
                    </div>

                    {{-- Stats --}}
                    <div class="flex w-full items-center gap-3 sm:w-auto sm:justify-end">
                        {{-- CPU --}}
                        <div class="text-center">
                            <div class="fi-section-header-description">{{ __('CPU') }}</div>
                            <x-filament::badge :color="$cpuColor">{{ $proc['cpu'] }}%</x-filament::badge>
                        </div>

                        {{-- Memory --}}
                        <div class="text-center">
                            <div class="fi-section-header-description">{{ __('MEM') }}</div>
                            <x-filament::badge :color="$memColor">{{ $proc['memory'] }}%</x-filament::badge>
                        </div>
                    </div>
                </div>
            @empty
                <div class="py-8 text-center">
                    <x-filament::icon icon="heroicon-o-cpu-chip" :size="\Filament\Support\Enums\IconSize::TwoExtraLarge" class="fi-color-gray fi-text-color-300 mb-3" />
                    <p class="fi-section-header-description">{{ __('No processes found') }}</p>
                </div>
            @endforelse
        </div>
    </x-filament::section>
</x-filament-widgets::widget>
