<x-filament-widgets::widget>
    <x-filament::section compact>
        <div
            wire:ignore
            x-data="{
                async refresh() {
                    const data = await $wire.getData();
                    const cpu = data.cpu || {};
                    const memory = data.memory || {};
                    const disk = data.disk || [];
                    const network = data.network || {};

                    this.createBar('cpu-chart', [
                        ['CPU ' + (cpu.usage || 0) + '%', cpu.usage || 0],
                        ['IO Wait ' + (cpu.iowait || 0) + '%', cpu.iowait || 0],
                    ]);

                    const memItems = [
                        ['Memory ' + (memory.usage || 0) + '%', memory.usage || 0, (memory.used_gb || 0) + '/' + (memory.total_gb || 0) + ' GB'],
                    ];
                    if (memory.has_swap) {
                        memItems.push(['Swap ' + (memory.swap_usage || 0) + '%', memory.swap_usage || 0, (memory.swap_used_gb || 0) + '/' + (memory.swap_total_gb || 0) + ' GB']);
                    }
                    this.createBar('memory-chart', memItems);

                    this.createBar('disk-chart', disk.map(p => [
                        (p.mount || '/') + ' ' + (p.usage || 0) + '%',
                        p.usage || 0,
                        (p.used_human || '0 B') + '/' + (p.total_human || '0 B'),
                    ]));

                    this.$refs.cpuInfo.textContent = (cpu.cores || '?') + ' cores \u00b7 ' + (cpu.model || '').substring(0, 30);
                    this.$refs.netTxSpeed.textContent = network.tx_speed || '0 B/s';
                    this.$refs.netTxTotal.textContent = '(' + (network.total_tx_human || '0 B') + ')';
                    this.$refs.netRxSpeed.textContent = network.rx_speed || '0 B/s';
                    this.$refs.netRxTotal.textContent = '(' + (network.total_rx_human || '0 B') + ')';
                },
                createBar(elId, items) {
                    const el = document.getElementById(elId);
                    if (!el || typeof Chart === 'undefined') return;

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

                    if (el._chart) {
                        el._chart.data.labels = labels;
                        el._chart.data.datasets[0].data = values;
                        el._chart.data.datasets[0].backgroundColor = values.map(v => barColor(v));
                        el._chart.data.datasets[1].data = remainder;
                        el._chart.data.datasets[1].backgroundColor = trackColor;
                        el._chart.options.scales.y.ticks.color = textColor;
                        el._chart.update('none');
                        return;
                    }

                    el._chart = new Chart(el, {
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
                                x: { stacked: true, display: false, max: 100 },
                                y: {
                                    stacked: true,
                                    display: true,
                                    grid: { display: false },
                                    ticks: { color: textColor, font: { size: 12, weight: 'bold' } },
                                    border: { display: false },
                                },
                            },
                            plugins: {
                                legend: { display: false },
                                tooltip: {
                                    callbacks: {
                                        label: (ctx) => {
                                            if (ctx.datasetIndex === 1) return null;
                                            return extra[ctx.dataIndex] || ctx.parsed.x + '%';
                                        },
                                    },
                                },
                            },
                        },
                    });
                },
                init() {
                    this.refresh();
                    setInterval(() => this.refresh(), 5000);
                },
            }"
            x-init="init()"
            class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4"
        >
            <div>
                <canvas id="cpu-chart" height="80"></canvas>
                <p x-ref="cpuInfo" class="mt-1 text-center text-xs text-gray-500 dark:text-gray-400"></p>
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
                    <span x-ref="netTxSpeed">0 B/s</span>
                    <span x-ref="netTxTotal" class="text-xs font-normal text-gray-500 dark:text-gray-400">(0 B)</span>
                </p>
                <p class="text-sm font-bold">
                    <span class="text-info-500">&darr;</span>
                    <span x-ref="netRxSpeed">0 B/s</span>
                    <span x-ref="netRxTotal" class="text-xs font-normal text-gray-500 dark:text-gray-400">(0 B)</span>
                </p>
            </div>
        </div>
    </x-filament::section>
</x-filament-widgets::widget>
