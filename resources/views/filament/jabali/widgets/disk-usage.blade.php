<x-filament-widgets::widget>
    <x-filament::section>
        <x-slot name="heading">{{ __('Disk Usage') }}</x-slot>
        <x-slot name="description">{{ $this->getData()['home'] }}</x-slot>

        @php
            $data = $this->getData();
            $percent = min(100, max(0, $data['percent']));
            $usedColor = match($data['color']) {
                'danger' => '#ef4444',
                'warning' => '#f59e0b',
                default => '#22c55e',
            };
        @endphp

        @if($data['has_quota'])
            <div
                x-data="{
                    chart: null,
                    value: {{ $percent }},
                    init() {
                        this.initChart();
                    },
                    initChart() {
                        if (typeof window.Chart === 'undefined') {
                            setTimeout(() => this.initChart(), 100);
                            return;
                        }
                        const isDark = document.documentElement.classList.contains('dark');
                        const ctx = this.$refs.chart.getContext('2d');
                        this.chart = new Chart(ctx, {
                            type: 'bar',
                            data: {
                                labels: [''],
                                datasets: [
                                    {
                                        label: '{{ __('Used') }}',
                                        data: [{{ $percent }}],
                                        backgroundColor: '{{ $usedColor }}',
                                        stack: 'total',
                                        borderRadius: { topLeft: 6, bottomLeft: 6, topRight: 0, bottomRight: 0 },
                                        borderSkipped: false,
                                    },
                                    {
                                        label: '{{ __('Free') }}',
                                        data: [{{ 100 - $percent }}],
                                        backgroundColor: isDark ? '#374151' : '#e5e7eb',
                                        stack: 'total',
                                        borderRadius: { topLeft: 0, bottomLeft: 0, topRight: 6, bottomRight: 6 },
                                        borderSkipped: false,
                                    }
                                ]
                            },
                            options: {
                                responsive: true,
                                maintainAspectRatio: false,
                                indexAxis: 'y',
                                plugins: {
                                    legend: { display: false },
                                    tooltip: {
                                        enabled: true,
                                        callbacks: {
                                            label: (context) => `${context.dataset.label}: ${Number(context.parsed.x).toFixed(1)}%`
                                        }
                                    }
                                },
                                scales: {
                                    x: {
                                        min: 0,
                                        max: 100,
                                        ticks: {
                                            color: isDark ? '#9ca3af' : '#6b7280',
                                            callback: (value) => `${value}%`,
                                            font: { size: 11 }
                                        },
                                        grid: { display: false }
                                    },
                                    y: {
                                        ticks: { display: false },
                                        grid: { display: false }
                                    }
                                }
                            }
                        });
                    },
                    destroy() {
                        this.chart?.destroy();
                    }
                }"
                wire:ignore
            >
                <div class="h-[70px] w-full">
                    <canvas x-ref="chart"></canvas>
                </div>
            </div>
        @endif

        @if($data['has_quota'] && $data['free'])
        <div class="flex flex-wrap items-center justify-center gap-6 pt-2">
            <div class="flex items-baseline gap-2">
                <div class="fi-section-header-heading">{{ $data['used'] }}</div>
                <div class="fi-section-header-description">{{ __('Used') }}</div>
            </div>
            <div class="flex items-baseline gap-2">
                <div class="fi-section-header-heading">{{ $data['free'] }}</div>
                <div class="fi-section-header-description">{{ __('Free') }}</div>
            </div>
            <div class="flex items-baseline gap-2">
                <div class="fi-section-header-heading">{{ $data['quota'] }}</div>
                <div class="fi-section-header-description">{{ __('Quota') }}</div>
            </div>
        </div>
        @else
        <div class="flex flex-wrap items-center justify-center gap-6 pt-2">
            <div class="flex items-baseline gap-2">
                <div class="fi-section-header-heading">{{ $data['used'] }}</div>
                <div class="fi-section-header-description">{{ __('Used') }}</div>
            </div>
            <div class="flex items-baseline gap-2">
                <div class="fi-section-header-heading">{{ $data['quota'] }}</div>
                <div class="fi-section-header-description">{{ __('Quota') }}</div>
            </div>
        </div>
        @endif
    </x-filament::section>
</x-filament-widgets::widget>
