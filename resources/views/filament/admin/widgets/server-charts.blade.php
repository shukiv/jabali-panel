@php
    $data = $this->getData();
    $cpu = $data['cpu'] ?? [];
    $memory = $data['memory'] ?? [];
    $disk = $data['disk'] ?? [];
    $network = $data['network'] ?? [];

    $barColor = function (float $pct): string {
        if ($pct >= 90) return '#EF4444';
        if ($pct >= 50) return '#F59E0B';
        return '#22C55E';
    };

    $cpuChart = [
        'names' => [__('CPU'), __('IO Wait')],
        'values' => [$cpu['usage'] ?? 0, $cpu['iowait'] ?? 0],
        'colors' => [$barColor($cpu['usage'] ?? 0), $barColor($cpu['iowait'] ?? 0)],
        'labels' => [__('CPU') . ' ' . ($cpu['usage'] ?? 0) . '%', __('IO Wait') . ' ' . ($cpu['iowait'] ?? 0) . '%'],
    ];

    $memNames = [__('Memory')];
    $memValues = [$memory['usage'] ?? 0];
    $memColors = [$barColor($memory['usage'] ?? 0)];
    $memLabels = [__('Memory') . ' ' . ($memory['usage'] ?? 0) . '% · ' . ($memory['used_gb'] ?? 0) . '/' . ($memory['total_gb'] ?? 0) . ' GB'];
    if ($memory['has_swap'] ?? false) {
        $memNames[] = __('Swap');
        $memValues[] = $memory['swap_usage'] ?? 0;
        $memColors[] = $barColor($memory['swap_usage'] ?? 0);
        $memLabels[] = __('Swap') . ' ' . ($memory['swap_usage'] ?? 0) . '% · ' . ($memory['swap_used_gb'] ?? 0) . '/' . ($memory['swap_total_gb'] ?? 0) . ' GB';
    }
    $memChart = ['names' => $memNames, 'values' => $memValues, 'colors' => $memColors, 'labels' => $memLabels];

    $diskNames = [];
    $diskValues = [];
    $diskColors = [];
    $diskLabels = [];
    foreach ($disk as $p) {
        $diskNames[] = $p['mount'];
        $diskValues[] = $p['usage'];
        $diskColors[] = $barColor($p['usage']);
        $diskLabels[] = $p['mount'] . ' ' . $p['usage'] . '% · ' . $p['used_human'] . '/' . $p['total_human'];
    }
    $diskChart = ['names' => $diskNames, 'values' => $diskValues, 'colors' => $diskColors, 'labels' => $diskLabels];
@endphp

<x-filament-widgets::widget wire:poll.5s="$refresh">
    <x-filament::section compact>
        <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4"
            x-data="{
                charts: {},
                get isDark() { return document.documentElement.classList.contains('dark') },
                init() {
                    this.$nextTick(() => this.buildAll());
                    new MutationObserver(() => this.rebuildAll()).observe(
                        document.documentElement, { attributes: true, attributeFilter: ['class'] }
                    );
                },
                buildAll() {
                    this.update(this.$refs.cpu, 'cpu', {{ \Illuminate\Support\Js::from($cpuChart) }});
                    this.update(this.$refs.mem, 'mem', {{ \Illuminate\Support\Js::from($memChart) }});
                    this.update(this.$refs.disk, 'disk', {{ \Illuminate\Support\Js::from($diskChart) }});
                },
                rebuildAll() {
                    Object.keys(this.charts).forEach(key => {
                        const entry = this.charts[key];
                        entry.chart.dispose();
                        entry.chart = this.createChart(entry.el, entry.data);
                    });
                },
                update(el, key, d) {
                    if (this.charts[key]) {
                        this.charts[key].data = d;
                        this.updateChart(this.charts[key].chart, d);
                    } else {
                        this.charts[key] = { el, data: d, chart: this.createChart(el, d) };
                    }
                },
                createChart(el, d) {
                    const chart = echarts.init(el, this.isDark ? 'jabali-dark' : 'shine', { renderer: 'canvas' });
                    chart.setOption(this.buildOption(d));
                    return chart;
                },
                updateChart(chart, d) {
                    chart.setOption(this.buildOption(d));
                },
                buildOption(d) {
                    return {
                        grid: { left: 4, right: 4, top: 2, bottom: 2 },
                        xAxis: { type: 'value', max: 100, show: false },
                        yAxis: { type: 'category', data: d.names, inverse: true, show: false },
                        series: [{
                            type: 'bar',
                            barWidth: '80%',
                            barGap: '10%',
                            showBackground: true,
                            backgroundStyle: { borderRadius: 0 },
                            animationDuration: 600,
                            data: d.values.map((v, i) => ({
                                value: v,
                                itemStyle: { color: d.colors[i], borderRadius: 0 },
                                label: {
                                    show: true,
                                    position: v > 3 ? 'insideLeft' : 'right',
                                    fontWeight: 'normal',
                                    fontSize: 11,
                                    formatter: d.labels[i],
                                },
                            })),
                        }],
                    };
                },
            }"
            x-init="$wire.on('$refresh', () => $nextTick(() => buildAll()))"
        >
            {{-- CPU & IO Wait --}}
            <div class="px-4 py-3">
                <div x-ref="cpu" class="h-[70px] w-full"></div>
            </div>

            {{-- Memory & Swap --}}
            <div class="px-4 py-3">
                <div x-ref="mem" class="h-[70px] w-full"></div>
            </div>

            {{-- Disk --}}
            <div class="px-4 py-3">
                @if(empty($disk))
                    <p class="flex h-[70px] items-center text-xs opacity-60">{{ __('No disk data') }}</p>
                @else
                    <div x-ref="disk" class="h-[70px] w-full"></div>
                @endif
            </div>

            {{-- Network --}}
            <div class="flex flex-col justify-center gap-1 px-4 py-3">
                <div class="flex items-center gap-1.5">
                    <span class="text-sm font-bold text-green-500">&uarr;</span>
                    <span class="text-sm font-bold">{{ $network['tx_speed'] ?? '0 B/s' }}</span>
                    <span class="text-xs opacity-60">({{ $network['total_tx_human'] ?? '0 B' }})</span>
                </div>
                <div class="flex items-center gap-1.5">
                    <span class="text-sm font-bold text-blue-400">&darr;</span>
                    <span class="text-sm font-bold">{{ $network['rx_speed'] ?? '0 B/s' }}</span>
                    <span class="text-xs opacity-60">({{ $network['total_rx_human'] ?? '0 B' }})</span>
                </div>
            </div>

        </div>
    </x-filament::section>
</x-filament-widgets::widget>
