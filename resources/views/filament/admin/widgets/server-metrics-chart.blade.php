<x-filament-widgets::widget>
    <x-filament::section compact>
        <div
            wire:ignore
            x-data="{
                async refresh() {
                    const m = await $wire.getMetrics();
                    this.renderBar('cpu-chart', [
                        ['CPU ' + m.cpu.usage + '%', m.cpu.usage],
                        ['IO Wait ' + m.cpu.iowait + '%', m.cpu.iowait],
                    ]);

                    const mem = [['RAM ' + m.memory.usage + '% (' + m.memory.used_gb + '/' + m.memory.total_gb + ' GB)', m.memory.usage]];
                    if (m.memory.has_swap) mem.push(['Swap ' + m.memory.swap_usage + '% (' + m.memory.swap_used_gb + '/' + m.memory.swap_total_gb + ' GB)', m.memory.swap_usage]);
                    this.renderBar('mem-chart', mem);

                    this.renderBar('disk-chart', (Array.isArray(m.disk) ? m.disk : [m.disk]).map(p =>
                        ['Disk ' + (p.mount || '/') + ' ' + p.usage + '% (' + p.used_human + '/' + p.total_human + ')', p.usage]
                    ));

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
                    const texts = items.map(i => i[0]);
                    const color = v => v >= 90 ? 'rgb(239,68,68)' : v >= 50 ? 'rgb(245,158,11)' : 'rgb(34,197,94)';
                    const isDark = document.documentElement.classList.contains('dark');
                    const track = isDark ? 'rgba(255,255,255,0.1)' : 'rgba(0,0,0,0.08)';
                    const textColor = isDark ? 'rgba(255,255,255,0.7)' : 'rgba(0,0,0,0.6)';

                    if (el._chart) {
                        el._chart.data.labels = texts.map(() => '');
                        el._chart.data.datasets[0].data = vals;
                        el._chart.data.datasets[0].backgroundColor = vals.map(v => color(v));
                        el._chart.data.datasets[0].barTexts = texts;
                        el._chart.data.datasets[1].data = rem;
                        el._chart.data.datasets[1].backgroundColor = track;
                        el._chart._isDark = isDark;
                        el._chart.update('none');
                        return;
                    }

                    el._chart = new Chart(el, {
                        type: 'bar',
                        data: {
                            labels: texts.map(() => ''),
                            datasets: [
                                { data: vals, backgroundColor: vals.map(v => color(v)), barTexts: texts, borderWidth: 0, borderSkipped: false, barThickness: 28 },
                                { data: rem, backgroundColor: track, borderWidth: 0, borderSkipped: false, barThickness: 28 },
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
                                y: { stacked: true, display: false },
                            },
                            plugins: { legend: { display: false }, tooltip: { enabled: false } },
                        },
                        plugins: [{
                            id: 'barLabels',
                            afterDatasetsDraw(chart) {
                                const ctx = chart.ctx;
                                const meta = chart.getDatasetMeta(0);
                                const barTexts = chart.data.datasets[0].barTexts || [];
                                const dark = chart._isDark !== undefined ? chart._isDark : document.documentElement.classList.contains('dark');
                                ctx.save();
                                ctx.font = 'bold 13px system-ui, sans-serif';
                                ctx.textBaseline = 'middle';
                                meta.data.forEach((bar, i) => {
                                    const text = barTexts[i] || '';
                                    if (!text) return;
                                    const w = bar.x - bar.base;
                                    if (w > 80) { ctx.fillStyle = '#fff'; ctx.fillText(text, bar.base + 8, bar.y); }
                                    else { ctx.fillStyle = dark ? 'rgba(255,255,255,0.7)' : 'rgba(0,0,0,0.6)'; ctx.fillText(text, bar.x + 6, bar.y); }
                                });
                                ctx.restore();
                            },
                        }],
                    });
                    el._chart._isDark = isDark;
                },
                init() {
                    const wait = () => {
                        if (typeof Chart !== 'undefined') { this.refresh(); setInterval(() => this.refresh(), 5000); }
                        else { setTimeout(wait, 100); }
                    };
                    wait();
                },
            }"
            x-init="init()"
            class="fi-wi-stats-overview-stats-ctn grid gap-2 md:grid-cols-4"
        >
            <div class="h-20"><canvas id="cpu-chart"></canvas></div>
            <div class="h-20"><canvas id="mem-chart"></canvas></div>
            <div class="h-20"><canvas id="disk-chart"></canvas></div>
            <div class="flex items-center justify-center gap-6">
                <div>
                    <div class="flex items-center gap-1 text-sm text-gray-500 dark:text-gray-400">
                        <x-filament::icon icon="heroicon-o-arrow-up" class="h-4 w-4 text-success-500" />
                        {{ __('Upload') }}
                    </div>
                    <p x-ref="txSpeed" class="text-xl font-bold">0 B/s</p>
                    <p x-ref="txTotal" class="text-xs text-success-500">0 B total</p>
                </div>
                <div>
                    <div class="flex items-center gap-1 text-sm text-gray-500 dark:text-gray-400">
                        <x-filament::icon icon="heroicon-o-arrow-down" class="h-4 w-4 text-info-500" />
                        {{ __('Download') }}
                    </div>
                    <p x-ref="rxSpeed" class="text-xl font-bold">0 B/s</p>
                    <p x-ref="rxTotal" class="text-xs text-info-500">0 B total</p>
                </div>
            </div>
        </div>
    </x-filament::section>
</x-filament-widgets::widget>
