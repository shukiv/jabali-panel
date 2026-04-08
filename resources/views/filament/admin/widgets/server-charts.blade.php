@php
    $data = $this->getData();
    $cpu = $data['cpu'] ?? [];
    $memory = $data['memory'] ?? [];
    $disk = $data['disk'] ?? [];
    $network = $data['network'] ?? [];
@endphp

<x-filament-widgets::widget wire:poll.5s="$refresh">
    <x-filament::section compact>
        <div
            x-data="{
                charts: {},
                init() {
                    this.$nextTick(() => {
                        this.createCharts();
                    });
                },
                createCharts() {
                    this.createBar('cpu-chart', @js([
                        ['CPU ' . ($cpu['usage'] ?? 0) . '%', $cpu['usage'] ?? 0],
                        ['IO Wait ' . ($cpu['iowait'] ?? 0) . '%', $cpu['iowait'] ?? 0],
                    ]));

                    this.createBar('memory-chart', @js(array_values(array_filter([
                        ['Memory ' . ($memory['usage'] ?? 0) . '%', $memory['usage'] ?? 0, ($memory['used_gb'] ?? 0) . '/' . ($memory['total_gb'] ?? 0) . ' GB'],
                        ($memory['has_swap'] ?? false) ? ['Swap ' . ($memory['swap_usage'] ?? 0) . '%', $memory['swap_usage'] ?? 0, ($memory['swap_used_gb'] ?? 0) . '/' . ($memory['swap_total_gb'] ?? 0) . ' GB'] : null,
                    ]))));

                    this.createBar('disk-chart', @js(array_map(fn ($p) => [
                        ($p['mount'] ?? '/') . ' ' . ($p['usage'] ?? 0) . '%',
                        $p['usage'] ?? 0,
                        ($p['used_human'] ?? '0 B') . '/' . ($p['total_human'] ?? '0 B'),
                    ], $disk)));
                },
                createBar(elId, items) {
                    const el = document.getElementById(elId);
                    if (!el || typeof Chart === 'undefined') return;

                    if (this.charts[elId]) {
                        this.charts[elId].destroy();
                    }

                    const labels = items.map(i => i[0]);
                    const values = items.map(i => i[1]);
                    const remainder = values.map(v => Math.max(0, 100 - v));
                    const extra = items.map(i => i[2] || '');

                    const barColor = (v) => {
                        if (v >= 90) return 'rgb(239, 68, 68)';
                        if (v >= 50) return 'rgb(245, 158, 11)';
                        return 'rgb(34, 197, 94)';
                    };

                    const isDark = document.documentElement.classList.contains('dark');
                    const trackColor = isDark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.06)';
                    const textColor = isDark ? 'rgba(255,255,255,0.9)' : 'rgba(0,0,0,0.8)';

                    this.charts[elId] = new Chart(el, {
                        type: 'bar',
                        data: {
                            labels: labels,
                            datasets: [
                                {
                                    data: values,
                                    backgroundColor: values.map(v => barColor(v)),
                                    borderRadius: 4,
                                    borderSkipped: false,
                                    barPercentage: 0.7,
                                    categoryPercentage: 0.85,
                                },
                                {
                                    data: remainder,
                                    backgroundColor: trackColor,
                                    borderRadius: 4,
                                    borderSkipped: false,
                                    barPercentage: 0.7,
                                    categoryPercentage: 0.85,
                                },
                            ],
                        },
                        options: {
                            indexAxis: 'y',
                            responsive: true,
                            maintainAspectRatio: false,
                            animation: { duration: 300 },
                            scales: {
                                x: {
                                    stacked: true,
                                    display: false,
                                    max: 100,
                                },
                                y: {
                                    stacked: true,
                                    display: true,
                                    grid: { display: false },
                                    ticks: {
                                        color: textColor,
                                        font: { size: 12, weight: 'bold' },
                                    },
                                    border: { display: false },
                                },
                            },
                            plugins: {
                                legend: { display: false },
                                tooltip: {
                                    callbacks: {
                                        label: (ctx) => {
                                            if (ctx.datasetIndex === 1) return null;
                                            const idx = ctx.dataIndex;
                                            return extra[idx] ? extra[idx] : ctx.parsed.x + '%';
                                        },
                                    },
                                },
                            },
                        },
                    });
                },
            }"
            x-init="init()"
            x-effect="createCharts()"
            class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4"
        >
            <div>
                <canvas id="cpu-chart" height="80"></canvas>
                <p class="mt-1 text-center text-xs text-gray-500 dark:text-gray-400">{{ $cpu['cores'] ?? '?' }} {{ __('cores') }} &middot; {{ Str::limit($cpu['model'] ?? '', 30) }}</p>
            </div>

            <div>
                <canvas id="memory-chart" height="80"></canvas>
            </div>

            <div>
                <canvas id="disk-chart" height="80"></canvas>
            </div>

            <div class="flex flex-col items-center justify-center gap-1">
                <p class="text-xs text-gray-500 dark:text-gray-400">{{ __('Network') }} &middot; {{ __('Current (Total)') }}</p>
                <p class="text-sm font-bold">
                    <span class="text-success-500">&uarr;</span>
                    {{ $network['tx_speed'] ?? '0 B/s' }}
                    <span class="text-xs font-normal text-gray-500 dark:text-gray-400">({{ $network['total_tx_human'] ?? '0 B' }})</span>
                </p>
                <p class="text-sm font-bold">
                    <span class="text-info-500">&darr;</span>
                    {{ $network['rx_speed'] ?? '0 B/s' }}
                    <span class="text-xs font-normal text-gray-500 dark:text-gray-400">({{ $network['total_rx_human'] ?? '0 B' }})</span>
                </p>
            </div>
        </div>
    </x-filament::section>
</x-filament-widgets::widget>
