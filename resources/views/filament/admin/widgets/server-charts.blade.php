@php
    $data = $this->getData();
    $cpu = $data['cpu'] ?? [];
    $memory = $data['memory'] ?? [];
    $disk = $data['disk'] ?? [];
    $network = $data['network'] ?? [];

    $cpuUsage = (float) ($cpu['usage'] ?? 0);
    $ioWait = (float) ($cpu['iowait'] ?? 0);
    $memUsage = (float) ($memory['usage'] ?? 0);
    $swapUsage = (float) ($memory['swap_usage'] ?? 0);
    $hasSwap = $memory['has_swap'] ?? false;

    $barColor = function (float $pct): string {
        if ($pct >= 90) return 'bg-danger-500';
        if ($pct >= 50) return 'bg-warning-500';
        return 'bg-success-500';
    };
@endphp

<x-filament-widgets::widget wire:poll.10s="$refresh">
    <x-filament::section compact>
        <div class="grid grid-cols-1 divide-y divide-gray-200 dark:divide-white/10 sm:grid-cols-2 sm:divide-x sm:divide-y-0 lg:grid-cols-4" aria-live="polite" aria-atomic="true">

            {{-- CPU --}}
            <div class="px-4 py-3">
                <div class="relative h-7 w-full overflow-hidden rounded bg-gray-200 dark:bg-white/10">
                    <div class="absolute inset-y-0 start-0 rounded {{ $barColor($cpuUsage) }}" style="width:{{ max($cpuUsage, 1) }}%"></div>
                    <div class="absolute inset-0 flex items-center px-2.5">
                        <span class="text-xs font-bold">{{ __('CPU') }} {{ $cpuUsage }}%</span>
                    </div>
                </div>
                <div class="relative mt-1.5 h-7 w-full overflow-hidden rounded bg-gray-200 dark:bg-white/10">
                    <div class="absolute inset-y-0 start-0 rounded {{ $barColor($ioWait) }}" style="width:{{ max($ioWait, 1) }}%"></div>
                    <div class="absolute inset-0 flex items-center px-2.5">
                        <span class="text-xs font-bold">{{ __('IO Wait') }} {{ $ioWait }}%</span>
                    </div>
                </div>
            </div>

            {{-- Memory --}}
            <div class="px-4 py-3">
                <div class="relative h-7 w-full overflow-hidden rounded bg-gray-200 dark:bg-white/10">
                    <div class="absolute inset-y-0 start-0 rounded {{ $barColor($memUsage) }}" style="width:{{ max($memUsage, 1) }}%"></div>
                    <div class="absolute inset-0 flex items-center justify-between px-2.5">
                        <span class="text-xs font-bold drop-shadow-sm">{{ __('Memory') }} {{ $memUsage }}%</span>
                        <span class="text-xs font-medium">{{ $memory['used_gb'] ?? 0 }}/{{ $memory['total_gb'] ?? 0 }} GB</span>
                    </div>
                </div>
                @if($hasSwap)
                <div class="relative mt-1.5 h-7 w-full overflow-hidden rounded bg-gray-200 dark:bg-white/10">
                    <div class="absolute inset-y-0 start-0 rounded {{ $barColor($swapUsage) }}" style="width:{{ max($swapUsage, 1) }}%"></div>
                    <div class="absolute inset-0 flex items-center justify-between px-2.5">
                        <span class="text-xs font-bold drop-shadow-sm">{{ __('Swap') }} {{ $swapUsage }}%</span>
                        <span class="text-xs font-medium">{{ $memory['swap_used_gb'] ?? 0 }}/{{ $memory['swap_total_gb'] ?? 0 }} GB</span>
                    </div>
                </div>
                @endif
            </div>

            {{-- Disk --}}
            <div class="px-4 py-3">
                @forelse($disk as $i => $partition)
                    @php $diskPct = (float) ($partition['usage'] ?? 0); @endphp
                    <div class="relative {{ $i > 0 ? 'mt-1.5' : '' }} h-7 w-full overflow-hidden rounded bg-gray-200 dark:bg-white/10">
                        <div class="absolute inset-y-0 start-0 rounded {{ $barColor($diskPct) }}" style="width:{{ max($diskPct, 1) }}%"></div>
                        <div class="absolute inset-0 flex items-center justify-between px-2.5">
                            <span class="text-xs font-bold drop-shadow-sm">{{ $partition['mount'] }} {{ $diskPct }}%</span>
                            <span class="text-xs font-medium">{{ $partition['used_human'] }}/{{ $partition['total_human'] }}</span>
                        </div>
                    </div>
                @empty
                    <p class="text-xs opacity-60">{{ __('No disk data') }}</p>
                @endforelse
            </div>

            {{-- Network --}}
            <div class="px-4 py-3">
                <p class="mb-1.5 text-center text-xs opacity-60">{{ __('Network') }} {{ __('Current (Total)') }}</p>
                <div class="flex items-center gap-1.5">
                    <span class="text-sm font-bold text-success-500">&uarr;</span>
                    <span class="text-sm font-bold">{{ $network['tx_speed'] ?? '0 B/s' }}</span>
                    <span class="text-xs opacity-60">({{ $network['total_tx_human'] ?? '0 B' }})</span>
                </div>
                <div class="mt-0.5 flex items-center gap-1.5">
                    <span class="text-sm font-bold text-info-500">&darr;</span>
                    <span class="text-sm font-bold">{{ $network['rx_speed'] ?? '0 B/s' }}</span>
                    <span class="text-xs opacity-60">({{ $network['total_rx_human'] ?? '0 B' }})</span>
                </div>
            </div>

        </div>
    </x-filament::section>
</x-filament-widgets::widget>
