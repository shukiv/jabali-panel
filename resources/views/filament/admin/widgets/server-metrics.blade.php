<x-filament-widgets::widget>
    <x-filament::section compact>
        <div
            wire:ignore
            x-data="{
                async refresh() {
                    const m = await $wire.getMetrics();
                    this.renderBar('cpu-chart', [
                        ['CPU', m.cpu.usage, m.cpu.usage + '%'],
                        ['IO Wait', m.cpu.iowait, m.cpu.iowait + '%'],
                    ]);

                    const mem = [['Memory', m.memory.usage, m.memory.usage + '%  ' + m.memory.used_gb + '/' + m.memory.total_gb + ' GB']];
                    if (m.memory.has_swap) mem.push(['Swap', m.memory.swap_usage, m.memory.swap_usage + '%  ' + m.memory.swap_used_gb + '/' + m.memory.swap_total_gb + ' GB']);
                    this.renderBar('mem-chart', mem);

                    this.renderBar('disk-chart', (Array.isArray(m.disk) ? m.disk : [m.disk]).map(p => [p.mount || '/', p.usage, p.usage + '%  ' + p.used_human + '/' + p.total_human]));

                    this.$refs.txSpeed.textContent = m.network.tx_speed;
                    this.$refs.txTotal.textContent = m.network.total_tx + ' total';
                    this.$refs.rxSpeed.textContent = m.network.rx_speed;
                    this.$refs.rxTotal.textContent = m.network.total_rx + ' total';
                },
                renderBar(id, items) {
                    const el = document.getElementById(id);
                    if (!el || typeof Chart === 'undefined') return;
                    const vals = items.map(i => Math.max(i[1], 1));
                    const rem = vals.map(v => Math.max(0, 100 - v));
                    const barTexts = items.map(i => i[2] || '');
                    const color = v => v >= 90 ? 'rgb(239,68,68)' : v >= 50 ? 'rgb(245,158,11)' : 'rgb(34,197,94)';
                    const isDark = document.documentElement.classList.contains('dark');

                    if (el._chart) {
                        el._chart.data.labels = items.map(i => i[0]);
                        el._chart.data.datasets[0].data = vals;
                        el._chart.data.datasets[0].backgroundColor = vals.map(v => color(v));
                        el._chart.data.datasets[0].barTexts = barTexts;
                        el._chart.data.datasets[1].data = rem;
                        el._chart.update('none');
                        return;
                    }

                    const barLabelPlugin = {
                        id: 'barLabels',
                        afterDatasetsDraw(chart) {
                            const ctx = chart.ctx;
                            const meta = chart.getDatasetMeta(0);
                            const texts = chart.data.datasets[0].barTexts || [];
                            ctx.save();
                            ctx.font = 'bold 11px system-ui, sans-serif';
                            ctx.textBaseline = 'middle';
                            meta.data.forEach((bar, i) => {
                                const text = texts[i] || '';
                                if (!text) return;
                                const textInBar = bar.x > bar.base + 60;
                                const x = textInBar ? bar.base + 8 : bar.x + 6;
                                ctx.fillStyle = textInBar ? '#fff' : (isDark ? 'rgba(255,255,255,0.7)' : 'rgba(0,0,0,0.6)');
                                ctx.fillText(text, x, bar.y);
                            });
                            ctx.restore();
                        },
                    };

                    el._chart = new Chart(el, {
                        type: 'bar',
                        data: {
                            labels: items.map(i => i[0]),
                            datasets: [
                                { data: vals, backgroundColor: vals.map(v => color(v)), barTexts: barTexts, borderWidth: 0, borderRadius: 4, borderSkipped: false, barPercentage: 0.6 },
                                { data: rem, backgroundColor: isDark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.06)', borderWidth: 0, borderRadius: 4, borderSkipped: false, barPercentage: 0.6 },
                            ],
                        },
                        options: {
                            indexAxis: 'y',
                            responsive: true,
                            maintainAspectRatio: false,
                            animation: { duration: 300 },
                            scales: {
                                x: { stacked: true, display: false, max: 100 },
                                y: { stacked: true, grid: { display: false }, border: { display: false }, afterFit: (scale) => { scale.width = 80; }, ticks: { color: isDark ? 'rgba(255,255,255,0.9)' : 'rgba(0,0,0,0.8)', font: { size: 11, weight: 'bold' } } },
                            },
                            plugins: { legend: { display: false }, tooltip: { enabled: false } },
                        },
                        plugins: [barLabelPlugin],
                    });
                },
                init() {
                    this.refresh();
                    setInterval(() => this.refresh(), 3000);
                },
            }"
            x-init="init()"
        >
            <div class="fi-wi-stats-overview-stats-ctn grid gap-6 md:grid-cols-4">
                <div><canvas id="cpu-chart" height="80"></canvas></div>
                <div><canvas id="mem-chart" height="80"></canvas></div>
                <div><canvas id="disk-chart" height="80"></canvas></div>
                <div class="flex flex-col items-center justify-center gap-3">
                    <div>
                        <x-filament::icon icon="heroicon-o-arrow-up" @class(['inline-block', 'h-4', 'w-4']) style="color: var(--success-500)" />
                        <span x-ref="txSpeed" class="fi-header-heading" style="font-size: 1.125rem">0 B/s</span>
                        <p x-ref="txTotal" style="font-size: 0.75rem; color: var(--success-500)">0 B total</p>
                    </div>
                    <div>
                        <x-filament::icon icon="heroicon-o-arrow-down" @class(['inline-block', 'h-4', 'w-4']) style="color: var(--info-500)" />
                        <span x-ref="rxSpeed" class="fi-header-heading" style="font-size: 1.125rem">0 B/s</span>
                        <p x-ref="rxTotal" style="font-size: 0.75rem; color: var(--info-500)">0 B total</p>
                    </div>
                </div>
            </div>
        </div>
    </x-filament::section>
</x-filament-widgets::widget>
