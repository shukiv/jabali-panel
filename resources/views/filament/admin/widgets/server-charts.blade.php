<x-filament-widgets::widget
    x-data="{
        poller: null,
        isRefreshing: false,
        lastUpdatedLabel: null,
        init() {
            this.requestRefresh();
            this.poller = setInterval(() => {
                this.requestRefresh();
            }, 10000);
        },
        async requestRefresh() {
            if (this.isRefreshing) {
                return;
            }
            if (document.visibilityState === 'hidden') {
                return;
            }
            this.isRefreshing = true;
            try {
                await $wire.refreshData();
                this.lastUpdatedLabel = new Date().toLocaleTimeString();
            } finally {
                this.isRefreshing = false;
            }
        },
        stop() {
            if (this.poller) {
                clearInterval(this.poller);
                this.poller = null;
            }
        }
    }"
    x-on:livewire:navigating.window="stop()"
    x-on:beforeunload.window="stop()"
>
    @php
        $data = $this->getData();
        $cpu = $data['cpu'] ?? [];
        $memory = $data['memory'] ?? [];
        $disk = $data['disk'] ?? [];
        $partitions = $disk['partitions'] ?? [];
        $load = $data['load'] ?? [];
        $uptime = $data['uptime'] ?? 'N/A';
        $range = $this->range;
        $historyPoints = match ($range) {
            '5m' => 30,
            '30m' => 180,
            'day' => 24,
            'week' => 28,
            'month' => 30,
            default => 30,
        };
        $historyIntervalSeconds = match ($range) {
            '5m' => 10,
            '30m' => 10,
            'day' => 3600,
            'week' => 21600,
            'month' => 86400,
            default => 60,
        };
        $historyLabelFormat = match ($range) {
            '5m' => 'H:i:s',
            '30m' => 'H:i:s',
            'day' => 'H:00',
            'week' => 'M d H:00',
            'month' => 'M d',
            default => 'H:i',
        };

        $cpuCores = max(1, (int)($cpu['cores'] ?? 1));
        $load1 = (float)($load['1min'] ?? 0);
        $load5 = (float)($load['5min'] ?? 0);
        $load15 = (float)($load['15min'] ?? 0);
        $ioWait = (float)($cpu['iowait'] ?? 0);
        $memUsage = $memory['usage'] ?? 0;
        $history = $this->getHistory(
            $load1,
            $ioWait,
            (float) $memUsage,
            (float) ($memory['swap_usage'] ?? 0),
            $partitions,
        );
        $historyLabels = $history['labels'] ?? [];
        $historyLoad = $history['load'] ?? [];
        $historyIoWait = $history['iowait'] ?? [];
        $historyMemory = $history['memory'] ?? [];
        $historySwap = $history['swap'] ?? [];
        // Memory values are in MB from agent
        $memUsedGB = ($memory['used'] ?? 0) / 1024;
        $memTotalGB = ($memory['total'] ?? 0) / 1024;
        $swapUsedGB = ($memory['swap_used'] ?? 0) / 1024;
        $swapTotalGB = ($memory['swap_total'] ?? 0) / 1024;
        $hasSwap = $swapTotalGB > 0;

        $diskMounts = collect($partitions)
            ->map(fn (array $partition) => $partition['mount'] ?? $partition['filesystem'] ?? null)
            ->filter()
            ->unique()
            ->values()
            ->all();
        $diskTotals = [];
        $diskUsedTotal = 0;
        $diskTotal = 0;
        foreach ($partitions as $partition) {
            $mount = $partition['mount'] ?? $partition['filesystem'] ?? null;
            if ($mount === null) {
                continue;
            }
            $used = (float) ($partition['used'] ?? 0);
            $total = (float) ($partition['total'] ?? 0);
            $diskTotals[$mount] = $total > 0 ? round($total / 1024 / 1024 / 1024, 2) : 0;
            $diskUsedTotal += $used;
            $diskTotal += $total;
        }
        $diskUsedTotalGB = $diskUsedTotal > 0 ? $diskUsedTotal / 1024 / 1024 / 1024 : 0;
        $diskTotalGB = $diskTotal > 0 ? $diskTotal / 1024 / 1024 / 1024 : 0;
        $diskUsedSeries = [];
        $diskFreeSeries = [];
        foreach ($diskMounts as $mount) {
            $partition = collect($partitions)->first(fn (array $row) => ($row['mount'] ?? $row['filesystem'] ?? null) === $mount);
            $used = $partition ? ((float) ($partition['used'] ?? 0)) / 1024 / 1024 / 1024 : 0;
            $total = $partition ? ((float) ($partition['total'] ?? 0)) / 1024 / 1024 / 1024 : 0;
            $diskUsedSeries[] = round($used, 2);
            $diskFreeSeries[] = round(max(0, $total - $used), 2);
        }
    @endphp

    <div class="mb-4 flex flex-wrap items-center gap-2">
        <x-filament::button
            size="sm"
            :color="$range === '5m' ? 'primary' : 'gray'"
            wire:click="setRange('5m')"
        >
            {{ __('Last 5 minutes') }}
        </x-filament::button>
        <x-filament::button
            size="sm"
            :color="$range === '30m' ? 'primary' : 'gray'"
            wire:click="setRange('30m')"
        >
            {{ __('Last 30 minutes') }}
        </x-filament::button>
        <x-filament::button
            size="sm"
            :color="$range === 'day' ? 'primary' : 'gray'"
            wire:click="setRange('day')"
        >
            {{ __('Last day') }}
        </x-filament::button>
        <x-filament::button
            size="sm"
            :color="$range === 'week' ? 'primary' : 'gray'"
            wire:click="setRange('week')"
        >
            {{ __('Last week') }}
        </x-filament::button>
        <x-filament::button
            size="sm"
            :color="$range === 'month' ? 'primary' : 'gray'"
            wire:click="setRange('month')"
        >
            {{ __('Last month') }}
        </x-filament::button>
        <div class="ml-auto fi-section-header-description">
            {{ __('Last updated') }}:
            <span x-text="lastUpdatedLabel ?? '—'"></span>
        </div>
    </div>
    @if(!empty($data['warning']))
        <div class="mb-3 fi-section-header-description fi-color-warning fi-text-color-600 dark:fi-text-color-400">
            {{ __('Agent unavailable, showing local metrics.') }}
        </div>
    @endif

    {{-- Main Charts Grid (3 columns) --}}
    <div
        class="grid grid-cols-1 gap-0 md:grid"
        style="grid-template-columns: 40% 40% 20%;"
    >
        {{-- CPU Usage Chart --}}
        <div class="grid h-[18rem] grid-rows-[1fr_auto] p-2 lg:h-[20rem]">
            <div
                x-data="{
                    labels: @js($historyLabels),
                    series: @js($historyLoad),
                    ioWaitSeries: @js($historyIoWait),
                    value: {{ $load1 }},
                    ioWaitValue: {{ $ioWait }},
                    maxValue: {{ $cpuCores }},
                    maxPoints: {{ $historyPoints }},
                    intervalSeconds: {{ $historyIntervalSeconds }},
                    labelFormat: @js($historyLabelFormat),
                    init() {
                        if (!this.labels.length || !this.series.length) {
                            this.seedSeries();
                        }
                        this.initChart();
                        const handleRange = (payload) => {
                            const normalized = Array.isArray(payload) ? (payload[0] ?? {}) : (payload ?? {});
                            if (normalized?.label_format) {
                                this.labelFormat = normalized.label_format;
                            }
                            if (normalized?.interval_seconds) {
                                this.intervalSeconds = normalized.interval_seconds;
                            }
                            this.applyHistory(normalized?.history, normalized?.history_points);
                        };
                        const handleUpdate = (payload) => {
                            const normalized = Array.isArray(payload) ? (payload[0] ?? {}) : (payload ?? {});
                            if (normalized?.history) {
                                this.applyHistory(normalized.history, normalized.history_points);
                                return;
                            }
                            const newValue = normalized?.load;
                            const ioWaitValue = normalized?.iowait;
                            const label = normalized?.label ?? null;
                            if (newValue !== undefined) {
                                this.pushPoint(newValue, ioWaitValue, label);
                            }
                        };
                        if (window.Livewire?.on) {
                            window.Livewire.on('server-charts-range-changed', handleRange);
                            window.Livewire.on('server-charts-updated', handleUpdate);
                        }
                        document.addEventListener('server-charts-range-changed', (event) => {
                            handleRange(event?.detail ?? {});
                        });
                        document.addEventListener('server-charts-updated', (event) => {
                            handleUpdate(event?.detail ?? {});
                        });
                    },
                    applyHistory(history, points) {
                        if (!history) {
                            return;
                        }
                        this.labels = history.labels ?? [];
                        this.series = history.load ?? [];
                        this.ioWaitSeries = history.iowait ?? [];
                        if (this.ioWaitSeries.length !== this.labels.length) {
                            this.ioWaitSeries = this.labels.map(() => this.ioWaitValue);
                        }
                        this.maxPoints = points ?? (this.labels.length || this.maxPoints);
                        if (history.label_format) {
                            this.labelFormat = history.label_format;
                        }
                        if (history.interval_seconds) {
                            this.intervalSeconds = history.interval_seconds;
                        }
                        this.updateChart();
                    },
                    seedSeries() {
                        const now = new Date();
                        for (let i = this.maxPoints - 1; i >= 0; i--) {
                            const stamp = new Date(now.getTime() - (i * this.intervalSeconds * 1000));
                            this.labels.push(this.formatTime(stamp));
                            this.series.push(this.value);
                            this.ioWaitSeries.push(this.ioWaitValue);
                        }
                    },
                    formatTime(date) {
                        const pad = (value) => String(value).padStart(2, '0');
                        const hours = pad(date.getHours());
                        const minutes = pad(date.getMinutes());
                        const seconds = pad(date.getSeconds());
                        const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
                        const month = months[date.getMonth()];
                        const day = pad(date.getDate());

                        switch (this.labelFormat) {
                            case 'H:i:s':
                                return `${hours}:${minutes}:${seconds}`;
                            case 'H:00':
                                return `${hours}:00`;
                            case 'M d H:00':
                                return `${month} ${day} ${hours}:00`;
                            case 'M d':
                                return `${month} ${day}`;
                            default:
                                return `${hours}:${minutes}:${seconds}`;
                        }
                    },
                    pushPoint(value, ioWaitValue, label) {
                        const nextLabel = label ?? this.formatTime(new Date());
                        const nextIoWait = ioWaitValue ?? this.ioWaitValue;
                        if (this.labels.length && this.labels[this.labels.length - 1] === nextLabel) {
                            this.series[this.series.length - 1] = value;
                            this.ioWaitSeries[this.ioWaitSeries.length - 1] = nextIoWait;
                            this.updateChart();
                            return;
                        }
                        this.series.push(value);
                        this.ioWaitSeries.push(nextIoWait);
                        this.labels.push(nextLabel);
                        if (this.series.length > this.maxPoints) {
                            this.series.shift();
                            this.ioWaitSeries.shift();
                            this.labels.shift();
                        }
                        this.updateChart();
                    },
                    axisInterval() {
                        return this.labels.length > 0 ? Math.max(0, Math.floor(this.labels.length / 6)) : 0;
                    },
                    tickStep() {
                        if (!this.labels.length) {
                            return 1;
                        }
                        return Math.max(1, Math.round(this.labels.length / 6));
                    },
                    tickLabel(index, maxTicks = 5) {
                        if (!this.labels.length) {
                            return '';
                        }
                        const step = Math.max(1, Math.ceil(this.labels.length / maxTicks));
                        return index % step === 0 ? (this.labels[index] ?? '') : '';
                    },
                    scaleBounds(series, minDefault = 0, maxDefault = null, paddingRatio = 0.15) {
                        if (!Array.isArray(series) || series.length === 0) {
                            return { min: minDefault, max: maxDefault };
                        }
                        const values = series.map((value) => Number(value)).filter((value) => Number.isFinite(value));
                        if (!values.length) {
                            return { min: minDefault, max: maxDefault };
                        }
                        const minValue = Math.min(...values);
                        const maxValue = Math.max(...values);
                        const range = Math.max(1, maxValue - minValue);
                        const padding = range * paddingRatio;
                        const min = Math.max(minDefault ?? minValue, minValue - padding);
                        const max = maxDefault !== null ? Math.min(maxDefault, maxValue + padding) : maxValue + padding;
                        return { min, max };
                    },
                    cloneArray(items) {
                        return Array.isArray(items) ? items.slice() : [];
                    },
                    getChart() {
                        return this.$refs.chart?.__chart ?? null;
                    },
                    setChart(instance) {
                        if (this.$refs.chart) {
                            this.$refs.chart.__chart = instance;
                        }
                    },
                    updateChart() {
                        const chart = this.getChart();
                        if (!chart) {
                            return;
                        }
                        const ratio = this.maxValue > 0 ? (this.series[this.series.length - 1] / this.maxValue) * 100 : this.series[this.series.length - 1];
                        const color = ratio >= 90 ? '#ef4444' : (ratio >= 70 ? '#f59e0b' : '#22c55e');
                        const areaColor = ratio >= 90 ? 'rgba(239,68,68,0.15)' : (ratio >= 70 ? 'rgba(245,158,11,0.15)' : 'rgba(34,197,94,0.15)');
                        const loadPeak = this.series.length ? Math.max(...this.series) : 0;
                        const loadMax = Math.max(this.maxValue, loadPeak);
                        const loadBounds = this.scaleBounds(this.series, 0, null, 0.2);
                        const ioWaitBounds = this.scaleBounds(this.ioWaitSeries, 0, 100, 0.2);
                        chart.data.labels = this.cloneArray(this.labels);
                        chart.data.datasets[0].data = this.cloneArray(this.series);
                        chart.data.datasets[0].borderColor = color;
                        chart.data.datasets[0].backgroundColor = areaColor;
                        chart.data.datasets[1].data = this.cloneArray(this.ioWaitSeries);
                        if (chart.options?.scales?.y) {
                            chart.options.scales.y.suggestedMin = loadBounds.min;
                            chart.options.scales.y.suggestedMax = Math.max(loadMax, loadBounds.max ?? loadMax);
                        }
                        if (chart.options?.scales?.y1) {
                            chart.options.scales.y1.suggestedMin = ioWaitBounds.min;
                            chart.options.scales.y1.suggestedMax = ioWaitBounds.max ?? 100;
                        }
                        chart.update('none');
                    },
                    initChart() {
                        if (typeof window.Chart === 'undefined') {
                            setTimeout(() => this.initChart(), 100);
                            return;
                        }
                        const isDark = document.documentElement.classList.contains('dark');
                        const ratio = this.maxValue > 0 ? (this.value / this.maxValue) * 100 : this.value;
                        const color = ratio >= 90 ? '#ef4444' : (ratio >= 70 ? '#f59e0b' : '#22c55e');
                        const areaColor = ratio >= 90 ? 'rgba(239,68,68,0.15)' : (ratio >= 70 ? 'rgba(245,158,11,0.15)' : 'rgba(34,197,94,0.15)');
                        const ioWaitColor = '#38bdf8';
                        const loadPeak = this.series.length ? Math.max(...this.series) : 0;
                        const loadMax = Math.max(this.maxValue, loadPeak);
                        const loadBounds = this.scaleBounds(this.series, 0, null, 0.2);
                        const ioWaitBounds = this.scaleBounds(this.ioWaitSeries, 0, 100, 0.2);
                        const ctx = this.$refs.chart.getContext('2d');
                        this.setChart(new Chart(ctx, {
                            type: 'line',
                            data: {
                                labels: this.cloneArray(this.labels),
                                datasets: [
                                    {
                                        label: 'Load',
                                        data: this.cloneArray(this.series),
                                        tension: 0.35,
                                        borderColor: color,
                                        backgroundColor: areaColor,
                                        fill: false,
                                        pointRadius: 2,
                                        pointHoverRadius: 3,
                                    },
                                    {
                                        label: 'IO wait',
                                        data: this.cloneArray(this.ioWaitSeries),
                                        tension: 0.35,
                                        borderColor: ioWaitColor,
                                        backgroundColor: 'transparent',
                                        borderDash: [6, 4],
                                        yAxisID: 'y1',
                                        pointRadius: 2,
                                        pointHoverRadius: 3,
                                    }
                                ]
                            },
                            options: {
                                responsive: true,
                                maintainAspectRatio: false,
                                interaction: { mode: 'index', intersect: false },
                                plugins: {
                                    legend: {
                                        display: true,
                                        position: 'bottom',
                                        labels: {
                                            color: isDark ? '#9ca3af' : '#6b7280',
                                            boxWidth: 10,
                                            boxHeight: 10,
                                            usePointStyle: true,
                                        }
                                    },
                                    tooltip: {
                                        enabled: true,
                                        callbacks: {
                                            label: (context) => {
                                                if (context.dataset.label === 'IO wait') {
                                                    return `IO wait: ${Number(context.parsed.y).toFixed(1)}%`;
                                                }
                                                return `Load: ${Number(context.parsed.y).toFixed(2)}`;
                                            }
                                        }
                                    }
                                },
                                scales: {
                                    x: {
                                        ticks: {
                                            color: isDark ? '#9ca3af' : '#6b7280',
                                            autoSkip: true,
                                            maxTicksLimit: 5,
                                            maxRotation: 0,
                                            minRotation: 0,
                                            callback: (value, index) => this.tickLabel(index, 5),
                                        },
                                        grid: { display: false }
                                    },
                                    y: {
                                        beginAtZero: false,
                                        suggestedMin: loadBounds.min,
                                        suggestedMax: Math.max(loadMax, loadBounds.max ?? loadMax),
                                        ticks: { color: isDark ? '#9ca3af' : '#6b7280' },
                                        grid: { color: isDark ? '#374151' : '#f3f4f6' }
                                    },
                                    y1: {
                                        beginAtZero: false,
                                        suggestedMin: ioWaitBounds.min,
                                        suggestedMax: ioWaitBounds.max ?? 100,
                                        position: 'right',
                                        grid: { drawOnChartArea: false },
                                        ticks: {
                                            color: isDark ? '#9ca3af' : '#6b7280',
                                            callback: (value) => `${value}%`,
                                        }
                                    }
                                }
                            }
                        }));
                    },
                    destroy() {
                        const chart = this.getChart();
                        if (chart) {
                            chart.destroy();
                        }
                        if (this.$refs.chart) {
                            this.$refs.chart.__chart = null;
                        }
                    }
                }"
                wire:ignore
            >
                <div class="relative h-full w-full min-h-[10rem] sm:min-h-[11rem] lg:min-h-[12rem]">
                    <canvas x-ref="chart" class="block h-full w-full"></canvas>
                </div>
            </div>
            <div class="mt-2 min-h-[3.5rem] flex items-center gap-2">
                <x-filament::icon icon="heroicon-o-chart-bar" color="primary" />
                <span class="fi-section-header-heading">{{ __('Load') }}</span>
                <span class="fi-section-header-description">{{ $cpuCores }} {{ __('cores') }} · {{ __('Load') }}: {{ $load1 }} / {{ $load5 }} / {{ $load15 }}</span>
            </div>
        </div>

        {{-- Memory Usage Chart --}}
        <div class="grid h-[18rem] grid-rows-[1fr_auto] p-2 lg:h-[20rem]">
            <div
                x-data="{
                    labels: @js($historyLabels),
                    series: @js($historyMemory),
                    swapSeries: @js($historySwap),
                    value: {{ $memUsage }},
                    swapValue: {{ $memory['swap_usage'] ?? 0 }},
                    hasSwap: {{ $hasSwap ? 'true' : 'false' }},
                    maxValue: 100,
                    maxPoints: {{ $historyPoints }},
                    intervalSeconds: {{ $historyIntervalSeconds }},
                    labelFormat: @js($historyLabelFormat),
                    init() {
                        if (!this.labels.length || !this.series.length) {
                            this.seedSeries();
                        }
                        this.initChart();
                        const handleRange = (payload) => {
                            const normalized = Array.isArray(payload) ? (payload[0] ?? {}) : (payload ?? {});
                            if (normalized?.label_format) {
                                this.labelFormat = normalized.label_format;
                            }
                            if (normalized?.interval_seconds) {
                                this.intervalSeconds = normalized.interval_seconds;
                            }
                            this.applyHistory(normalized?.history, normalized?.history_points);
                        };
                        const handleUpdate = (payload) => {
                            const normalized = Array.isArray(payload) ? (payload[0] ?? {}) : (payload ?? {});
                            if (normalized?.history) {
                                this.applyHistory(normalized.history, normalized.history_points);
                                return;
                            }
                            const newValue = normalized?.memory;
                            const swapValue = normalized?.swap;
                            const label = normalized?.label ?? null;
                            if (newValue !== undefined) {
                                this.pushPoint(newValue, swapValue, label);
                            }
                        };
                        if (window.Livewire?.on) {
                            window.Livewire.on('server-charts-range-changed', handleRange);
                            window.Livewire.on('server-charts-updated', handleUpdate);
                        }
                        document.addEventListener('server-charts-range-changed', (event) => {
                            handleRange(event?.detail ?? {});
                        });
                        document.addEventListener('server-charts-updated', (event) => {
                            handleUpdate(event?.detail ?? {});
                        });
                    },
                    applyHistory(history, points) {
                        if (!history) {
                            return;
                        }
                        this.labels = history.labels ?? [];
                        this.series = history.memory ?? [];
                        this.swapSeries = history.swap ?? [];
                        if (this.hasSwap && this.swapSeries.length !== this.labels.length) {
                            this.swapSeries = this.labels.map(() => 0);
                        }
                        this.maxPoints = points ?? (this.labels.length || this.maxPoints);
                        this.updateChart();
                    },
                    seedSeries() {
                        const now = new Date();
                        for (let i = this.maxPoints - 1; i >= 0; i--) {
                            const stamp = new Date(now.getTime() - (i * this.intervalSeconds * 1000));
                            this.labels.push(this.formatTime(stamp));
                            this.series.push(this.value);
                            if (this.hasSwap) {
                                this.swapSeries.push(this.swapValue);
                            }
                        }
                    },
                    formatTime(date) {
                        const pad = (value) => String(value).padStart(2, '0');
                        const hours = pad(date.getHours());
                        const minutes = pad(date.getMinutes());
                        const seconds = pad(date.getSeconds());
                        const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
                        const month = months[date.getMonth()];
                        const day = pad(date.getDate());

                        switch (this.labelFormat) {
                            case 'H:i:s':
                                return `${hours}:${minutes}:${seconds}`;
                            case 'H:00':
                                return `${hours}:00`;
                            case 'M d H:00':
                                return `${month} ${day} ${hours}:00`;
                            case 'M d':
                                return `${month} ${day}`;
                            default:
                                return `${hours}:${minutes}:${seconds}`;
                        }
                    },
                    pushPoint(value, swapValue, label) {
                        const nextLabel = label ?? this.formatTime(new Date());
                        const nextSwap = swapValue ?? this.swapValue ?? 0;
                        if (this.labels.length && this.labels[this.labels.length - 1] === nextLabel) {
                            this.series[this.series.length - 1] = value;
                            if (this.hasSwap) {
                                this.swapSeries[this.swapSeries.length - 1] = nextSwap;
                            }
                            this.updateChart();
                            return;
                        }
                        this.series.push(value);
                        this.labels.push(nextLabel);
                        if (this.hasSwap) {
                            this.swapSeries.push(nextSwap);
                        }
                        if (this.series.length > this.maxPoints) {
                            this.series.shift();
                            this.labels.shift();
                            if (this.hasSwap) {
                                this.swapSeries.shift();
                            }
                        }
                        this.updateChart();
                    },
                    axisInterval() {
                        return this.labels.length > 0 ? Math.max(0, Math.floor(this.labels.length / 6)) : 0;
                    },
                    tickStep() {
                        if (!this.labels.length) {
                            return 1;
                        }
                        return Math.max(1, Math.round(this.labels.length / 6));
                    },
                    tickLabel(index, maxTicks = 5) {
                        if (!this.labels.length) {
                            return '';
                        }
                        const step = Math.max(1, Math.ceil(this.labels.length / maxTicks));
                        return index % step === 0 ? (this.labels[index] ?? '') : '';
                    },
                    scaleBounds(series, minDefault = 0, maxDefault = 100, padding = 5) {
                        if (!Array.isArray(series) || series.length === 0) {
                            return { min: minDefault, max: maxDefault };
                        }
                        const values = series.map((value) => Number(value)).filter((value) => Number.isFinite(value));
                        if (!values.length) {
                            return { min: minDefault, max: maxDefault };
                        }
                        const minValue = Math.min(...values);
                        const maxValue = Math.max(...values);
                        const min = Math.max(minDefault ?? minValue, minValue - padding);
                        const max = maxDefault !== null ? Math.min(maxDefault, maxValue + padding) : maxValue + padding;
                        return { min, max };
                    },
                    cloneArray(items) {
                        return Array.isArray(items) ? items.slice() : [];
                    },
                    getChart() {
                        return this.$refs.chart?.__chart ?? null;
                    },
                    setChart(instance) {
                        if (this.$refs.chart) {
                            this.$refs.chart.__chart = instance;
                        }
                    },
                    updateChart() {
                        const chart = this.getChart();
                        if (!chart) {
                            return;
                        }
                        const value = this.series[this.series.length - 1];
                        const color = value >= 90 ? '#ef4444' : (value >= 70 ? '#f59e0b' : '#22c55e');
                        const areaColor = value >= 90 ? 'rgba(239,68,68,0.15)' : (value >= 70 ? 'rgba(245,158,11,0.15)' : 'rgba(34,197,94,0.15)');
                        const memoryBounds = this.scaleBounds(this.series, 0, 100, 5);
                        const swapBounds = this.scaleBounds(this.swapSeries, 0, 100, 5);
                        chart.data.labels = this.cloneArray(this.labels);
                        chart.data.datasets[0].data = this.cloneArray(this.series);
                        chart.data.datasets[0].borderColor = color;
                        chart.data.datasets[0].backgroundColor = areaColor;
                        if (this.hasSwap && chart.data.datasets[1]) {
                            chart.data.datasets[1].data = this.cloneArray(this.swapSeries);
                        }
                        if (chart.options?.scales?.y) {
                            chart.options.scales.y.suggestedMin = memoryBounds.min;
                            chart.options.scales.y.suggestedMax = memoryBounds.max ?? 100;
                        }
                        if (chart.options?.scales?.y1) {
                            chart.options.scales.y1.suggestedMin = swapBounds.min;
                            chart.options.scales.y1.suggestedMax = swapBounds.max ?? 100;
                        }
                        chart.update('none');
                    },
                    initChart() {
                        if (typeof window.Chart === 'undefined') {
                            setTimeout(() => this.initChart(), 100);
                            return;
                        }
                        const isDark = document.documentElement.classList.contains('dark');
                        const color = this.value >= 90 ? '#ef4444' : (this.value >= 70 ? '#f59e0b' : '#22c55e');
                        const areaColor = this.value >= 90 ? 'rgba(239,68,68,0.15)' : (this.value >= 70 ? 'rgba(245,158,11,0.15)' : 'rgba(34,197,94,0.15)');
                        const ctx = this.$refs.chart.getContext('2d');
                        const memoryBounds = this.scaleBounds(this.series, 0, 100, 5);
                        const swapBounds = this.scaleBounds(this.swapSeries, 0, 100, 5);
                        const datasets = [
                            {
                                label: 'Memory',
                                data: this.series,
                                tension: 0.35,
                                borderColor: color,
                                backgroundColor: areaColor,
                                fill: false,
                                yAxisID: 'y',
                                pointRadius: 2,
                                pointHoverRadius: 3,
                            }
                        ];
                        if (this.hasSwap) {
                            datasets.push({
                                label: 'Swap',
                                data: this.swapSeries,
                                tension: 0.35,
                                borderColor: '#3b82f6',
                                backgroundColor: 'rgba(59,130,246,0.12)',
                                borderDash: [6, 4],
                                fill: false,
                                yAxisID: 'y1',
                                pointRadius: 2,
                                pointHoverRadius: 3,
                            });
                        }
                        this.setChart(new Chart(ctx, {
                            type: 'line',
                            data: {
                                labels: this.cloneArray(this.labels),
                                datasets
                            },
                            options: {
                                responsive: true,
                                maintainAspectRatio: false,
                                interaction: { mode: 'index', intersect: false },
                                plugins: {
                                    legend: {
                                        display: true,
                                        position: 'bottom',
                                        labels: {
                                            color: isDark ? '#9ca3af' : '#6b7280',
                                            boxWidth: 10,
                                            boxHeight: 10,
                                            usePointStyle: true,
                                        }
                                    },
                                    tooltip: {
                                        enabled: true,
                                        callbacks: {
                                            label: (context) => {
                                                if (context.dataset.label === 'Swap') {
                                                    return `Swap: ${Number(context.parsed.y).toFixed(1)}%`;
                                                }
                                                return `Memory: ${Number(context.parsed.y).toFixed(1)}%`;
                                            }
                                        }
                                    }
                                },
                                scales: {
                                    x: {
                                        ticks: {
                                            color: isDark ? '#9ca3af' : '#6b7280',
                                            autoSkip: true,
                                            maxTicksLimit: 5,
                                            maxRotation: 0,
                                            minRotation: 0,
                                            callback: (value, index) => this.tickLabel(index, 5),
                                        },
                                        grid: { display: false }
                                    },
                                    y: {
                                        beginAtZero: false,
                                        suggestedMin: memoryBounds.min,
                                        suggestedMax: memoryBounds.max ?? 100,
                                        ticks: { color: isDark ? '#9ca3af' : '#6b7280' },
                                        grid: { color: isDark ? '#374151' : '#f3f4f6' }
                                    },
                                    y1: {
                                        beginAtZero: false,
                                        suggestedMin: swapBounds.min,
                                        suggestedMax: swapBounds.max ?? 100,
                                        position: 'right',
                                        grid: { drawOnChartArea: false },
                                        ticks: {
                                            color: isDark ? '#9ca3af' : '#6b7280',
                                            callback: (value) => `${value}%`,
                                        }
                                    }
                                }
                            }
                        }));
                    },
                    destroy() {
                        const chart = this.getChart();
                        if (chart) {
                            chart.destroy();
                        }
                        if (this.$refs.chart) {
                            this.$refs.chart.__chart = null;
                        }
                    }
                }"
                wire:ignore
            >
                <div class="relative h-full w-full min-h-[10rem] sm:min-h-[11rem] lg:min-h-[12rem]">
                    <canvas x-ref="chart" class="block h-full w-full"></canvas>
                </div>
            </div>
            <div class="mt-2 min-h-[3.5rem] flex items-center gap-2">
                <x-filament::icon icon="heroicon-o-server" color="info" />
                <span class="fi-section-header-heading">{{ __('Memory') }}</span>
                <span class="fi-section-header-description">
                    {{ number_format($memUsedGB, 1) }} GB {{ __('used') }} / {{ number_format($memTotalGB, 1) }} GB {{ __('total') }}
                    @if($hasSwap)
                        · {{ __('Swap') }} {{ number_format($swapUsedGB, 1) }} GB / {{ number_format($swapTotalGB, 1) }} GB
                    @endif
                </span>
            </div>
        </div>

        {{-- Disk Usage Chart --}}
        @if(!empty($diskMounts))
        <div class="grid h-[18rem] grid-rows-[1fr_auto] p-2 lg:h-[20rem]">
                <div
                    x-data="{
                        mounts: @js($diskMounts),
                        usedSeries: @js($diskUsedSeries),
                        freeSeries: @js($diskFreeSeries),
                        totals: @js($diskTotals),
                        labels: {
                            used: @js(__('used')),
                            free: @js(__('free')),
                            total: @js(__('total')),
                        },
                        init() {
                            this.initChart();
                        },
                        withOpacity(color, opacity) {
                            const hex = color.replace('#', '');
                            const bigint = parseInt(hex, 16);
                            const r = (bigint >> 16) & 255;
                            const g = (bigint >> 8) & 255;
                            const b = bigint & 255;
                            return `rgba(${r}, ${g}, ${b}, ${opacity})`;
                        },
                        cloneArray(items) {
                            return Array.isArray(items) ? items.slice() : [];
                        },
                        getChart() {
                            return this.$refs.chart?.__chart ?? null;
                        },
                        setChart(instance) {
                            if (this.$refs.chart) {
                                this.$refs.chart.__chart = instance;
                            }
                        },
                        initChart() {
                            if (typeof window.Chart === 'undefined') {
                                setTimeout(() => this.initChart(), 100);
                                return;
                            }
                            const isDark = document.documentElement.classList.contains('dark');
                            const ctx = this.$refs.chart.getContext('2d');
                            this.setChart(new Chart(ctx, {
                                type: 'bar',
                                data: {
                                    labels: this.cloneArray(this.mounts),
                                    datasets: [
                                        {
                                            label: 'Used',
                                            data: this.cloneArray(this.usedSeries),
                                            backgroundColor: '#f59e0b',
                                            stack: 'disk',
                                            borderWidth: 0,
                                        },
                                        {
                                            label: 'Free',
                                            data: this.cloneArray(this.freeSeries),
                                            backgroundColor: this.withOpacity('#22c55e', 0.35),
                                            stack: 'disk',
                                            borderWidth: 0,
                                        }
                                    ]
                                },
                                options: {
                                    responsive: true,
                                    maintainAspectRatio: false,
                                    interaction: { mode: 'index', intersect: false },
                                plugins: {
                                    legend: {
                                        display: true,
                                        position: 'bottom',
                                        labels: {
                                            color: isDark ? '#9ca3af' : '#6b7280',
                                            boxWidth: 10,
                                            boxHeight: 10,
                                            usePointStyle: true,
                                        }
                                    },
                                    tooltip: {
                                        enabled: true,
                                        callbacks: {
                                            label: (context) => {
                                                const mount = context.label ?? '';
                                                const used = Number(this.usedSeries[context.dataIndex] ?? 0);
                                                const free = Number(this.freeSeries[context.dataIndex] ?? 0);
                                                const total = Number(this.totals[mount] ?? (used + free));
                                                if (context.dataset.label === 'Used') {
                                                    return `Used: ${used.toFixed(2)} GB`;
                                                }
                                                if (context.dataset.label === 'Free') {
                                                    return `Free: ${free.toFixed(2)} GB`;
                                                }
                                                return `Total: ${total.toFixed(2)} GB`;
                                            },
                                            afterBody: (items) => {
                                                const index = items?.[0]?.dataIndex ?? 0;
                                                const mount = items?.[0]?.label ?? '';
                                                const used = Number(this.usedSeries[index] ?? 0);
                                                const free = Number(this.freeSeries[index] ?? 0);
                                                const total = Number(this.totals[mount] ?? (used + free));
                                                return `Total: ${total.toFixed(2)} GB`;
                                            }
                                        }
                                    }
                                },
                                    scales: {
                                        x: {
                                            stacked: true,
                                            ticks: { color: isDark ? '#9ca3af' : '#6b7280', fontSize: 10 },
                                            grid: { display: false }
                                        },
                                    y: {
                                        stacked: true,
                                        beginAtZero: true,
                                        ticks: {
                                            color: isDark ? '#9ca3af' : '#6b7280',
                                            callback: (value) => `${value} GB`
                                        },
                                        grid: { color: isDark ? '#374151' : '#f3f4f6' }
                                    }
                                }
                            }
                            }));
                        },
                        destroy() {
                            const chart = this.getChart();
                            if (chart) {
                                chart.destroy();
                            }
                            if (this.$refs.chart) {
                                this.$refs.chart.__chart = null;
                            }
                        }
                    }"
                    wire:ignore
                >
                <div class="relative h-full w-full min-h-[10rem] sm:min-h-[11rem] lg:min-h-[12rem]">
                    <canvas x-ref="chart" class="block h-full w-full"></canvas>
                </div>
                </div>
            <div class="mt-2 min-h-[3.5rem] flex items-center gap-2">
                <x-filament::icon icon="heroicon-o-circle-stack" color="warning" />
                <span class="fi-section-header-heading">{{ __('Disk Usage') }}</span>
                <span class="fi-section-header-description">{{ number_format($diskUsedTotalGB, 1) }} GB {{ __('used') }} / {{ number_format($diskTotalGB, 1) }} GB {{ __('total') }}</span>
            </div>
            </div>
        @endif
    </div>

</x-filament-widgets::widget>
