<x-filament-widgets::widget>
    <x-filament::section>
        <x-slot name="heading">{{ __('Usage') }}</x-slot>

        <div
            wire:ignore
            x-data="{
                async refresh() {
                    const data = await $wire.getChartData();
                    this.renderBar('usage-disk-chart', data.disk);
                    this.renderBar('usage-bw-chart', data.bandwidth);
                    this.$refs.limDomains.textContent = data.limits.domains;
                    this.$refs.limDatabases.textContent = data.limits.databases;
                    this.$refs.limMailboxes.textContent = data.limits.mailboxes;
                },
                renderBar(id, items) {
                    const el = document.getElementById(id);
                    if (!el || typeof Chart === 'undefined') return;
                    const vals = items.map(i => Math.max(i[1], 1));
                    const rem = vals.map(v => Math.max(0, 100 - v));
                    const barTexts = items.map(i => i[2] || '');
                    const color = v => v >= 90 ? 'rgb(239,68,68)' : v >= 70 ? 'rgb(245,158,11)' : 'rgb(34,197,94)';
                    const isDark = document.documentElement.classList.contains('dark');
                    const trackColor = isDark ? 'rgba(255,255,255,0.1)' : 'rgba(0,0,0,0.08)';
                    const textColor = isDark ? 'rgba(255,255,255,0.9)' : 'rgba(0,0,0,0.8)';

                    if (el._chart) {
                        el._chart.data.labels = items.map(i => i[0]);
                        el._chart.data.datasets[0].data = vals;
                        el._chart.data.datasets[0].backgroundColor = vals.map(v => color(v));
                        el._chart.data.datasets[0].barTexts = barTexts;
                        el._chart.data.datasets[1].data = rem;
                        el._chart.data.datasets[1].backgroundColor = trackColor;
                        el._chart.options.scales.y.ticks.color = textColor;
                        el._chart._isDark = isDark;
                        el._chart.update('none');
                        return;
                    }

                    el._chart = new Chart(el, {
                        type: 'bar',
                        data: {
                            labels: items.map(i => i[0]),
                            datasets: [
                                { data: vals, backgroundColor: vals.map(v => color(v)), barTexts: barTexts, borderWidth: 0, borderRadius: 0, borderSkipped: false, barPercentage: 0.9, categoryPercentage: 0.8 },
                                { data: rem, backgroundColor: trackColor, borderWidth: 0, borderRadius: 0, borderSkipped: false, barPercentage: 0.9, categoryPercentage: 0.8 },
                            ],
                        },
                        options: {
                            indexAxis: 'y',
                            responsive: true,
                            maintainAspectRatio: false,
                            animation: { duration: 300 },
                            layout: { padding: 0 },
                            scales: {
                                x: { stacked: true, display: false, max: 100 },
                                y: { stacked: true, grid: { display: false }, border: { display: false }, afterFit: (scale) => { scale.width = 90; }, ticks: { color: textColor, font: { size: 11, weight: 'bold' } } },
                            },
                            plugins: { legend: { display: false }, tooltip: { enabled: false } },
                        },
                        plugins: [{
                            id: 'barLabels',
                            afterDatasetsDraw(chart) {
                                const ctx = chart.ctx;
                                const meta = chart.getDatasetMeta(0);
                                const texts = chart.data.datasets[0].barTexts || [];
                                const dark = chart._isDark !== undefined ? chart._isDark : document.documentElement.classList.contains('dark');
                                ctx.save();
                                ctx.font = 'bold 11px system-ui, sans-serif';
                                ctx.textBaseline = 'middle';
                                meta.data.forEach((bar, i) => {
                                    const text = texts[i] || '';
                                    if (!text) return;
                                    const barWidth = bar.x - bar.base;
                                    const textInBar = barWidth > 60;
                                    const x = textInBar ? bar.base + 8 : bar.x + 6;
                                    ctx.fillStyle = textInBar ? '#fff' : (dark ? 'rgba(255,255,255,0.7)' : 'rgba(0,0,0,0.6)');
                                    ctx.fillText(text, x, bar.y);
                                });
                                ctx.restore();
                            },
                        }],
                    });
                    el._chart._isDark = isDark;
                },
                init() {
                    const waitForChart = () => {
                        if (typeof Chart !== 'undefined') {
                            this.refresh();
                        } else {
                            setTimeout(waitForChart, 100);
                        }
                    };
                    waitForChart();
                },
            }"
            x-init="init()"
        >
            <div style="position: relative; height: 70px"><canvas id="usage-disk-chart"></canvas></div>
            <div style="position: relative; height: 70px"><canvas id="usage-bw-chart"></canvas></div>
            <div class="fi-wi-stats-overview-stats-ctn grid gap-2 md:grid-cols-3" style="margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--gray-200)">
                <div style="text-align: center">
                    <span x-ref="limDomains" style="font-size: 1.1rem; font-weight: 700">0</span>
                    <p style="font-size: 0.75rem; opacity: 0.6">{{ __('Domains') }}</p>
                </div>
                <div style="text-align: center">
                    <span x-ref="limDatabases" style="font-size: 1.1rem; font-weight: 700">0</span>
                    <p style="font-size: 0.75rem; opacity: 0.6">{{ __('Databases') }}</p>
                </div>
                <div style="text-align: center">
                    <span x-ref="limMailboxes" style="font-size: 1.1rem; font-weight: 700">0</span>
                    <p style="font-size: 0.75rem; opacity: 0.6">{{ __('Mailboxes') }}</p>
                </div>
            </div>
        </div>
    </x-filament::section>
</x-filament-widgets::widget>
